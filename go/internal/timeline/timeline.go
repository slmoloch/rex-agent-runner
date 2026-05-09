// Package timeline is the permanent queryable store for agent events.
//
// Events are written to both hourly JSONL files (audit log, eligible for
// retention/cleanup) and to this SQLite database (indexed, permanent). The
// JSONL files are authoritative for rebuilding the DB after corruption.
//
// Schema matches rex_timeline.py so on-disk databases round-trip between
// implementations.
package timeline

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; registers as "sqlite"
)

// ErrStaleSchema is returned by Open when the events table exists but is
// missing columns this build expects. The user has to run
// `rex timeline rebuild` to reindex from the JSONL audit log — we never
// rewrite their on-disk schema in place.
var ErrStaleSchema = errors.New("timeline: events.db schema is out of date; run `rex timeline rebuild` to reindex")

const schema = `
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    session TEXT,
    session_id TEXT,
    trigger TEXT,
    prompt_preview TEXT,
    response_preview TEXT,
    cost_usd REAL,
    duration_ms INTEGER,
    num_turns INTEGER,
    caller_session TEXT,
    turns TEXT,
    rex_user_sends TEXT
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_sid ON events(session_id);
`

// Row mirrors the events table. Turns/RexUserSends are stored as JSON text
// and decoded on read so callers get native types.
type Row struct {
	Timestamp       string  `json:"timestamp"`
	Session         string  `json:"session,omitempty"`
	SessionID       string  `json:"session_id,omitempty"`
	Trigger         string  `json:"trigger,omitempty"`
	PromptPreview   string  `json:"prompt_preview,omitempty"`
	ResponsePreview string  `json:"response_preview,omitempty"`
	CostUSD         float64 `json:"cost_usd,omitempty"`
	DurationMS      int     `json:"duration_ms,omitempty"`
	NumTurns        int     `json:"num_turns,omitempty"`
	CallerSession   string  `json:"caller_session,omitempty"`
	Turns           any     `json:"turns,omitempty"`
	RexUserSends    any     `json:"rex_user_sends,omitempty"`
}

type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database and applies the schema. If the
// table exists but is missing columns from the current schema (e.g. the
// `turns` column added when tool-call clipping was reworked), Open returns
// ErrStaleSchema rather than rewriting the on-disk layout — the user is
// expected to run `rex timeline rebuild` to reindex.
func Open(dbPath string) (*Store, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	cols, err := tableColumns(db, "events")
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, ok := cols["turns"]; !ok {
		db.Close()
		return nil, ErrStaleSchema
	}
	return &Store{db: db}, nil
}

// openForRebuild opens the database without enforcing the current schema —
// the caller (Rebuild) is about to drop and recreate the events table, so
// it must be allowed in even when the on-disk layout is out of date.
func openForRebuild(dbPath string) (*Store, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func openDB(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	// _busy_timeout + WAL gives sane behaviour under concurrent appenders.
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Keep the pool small — one database, one writer at a time.
	db.SetMaxOpenConns(1)
	return db, nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

// Insert appends one event row.
func (s *Store) Insert(r Row) error {
	turns, err := encodeJSON(r.Turns)
	if err != nil {
		return err
	}
	sends, err := encodeJSON(r.RexUserSends)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO events (
			timestamp, session, session_id, trigger,
			prompt_preview, response_preview,
			cost_usd, duration_ms, num_turns, caller_session,
			turns, rex_user_sends
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Timestamp, r.Session, r.SessionID, r.Trigger,
		r.PromptPreview, r.ResponsePreview,
		r.CostUSD, r.DurationMS, r.NumTurns, r.CallerSession,
		turns, sends,
	)
	return err
}

// Query returns rows matching the window. Exactly one of days or since should
// be used; `since` takes precedence when non-empty.
func (s *Store) Query(days int, since string) ([]Row, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if since != "" {
		rows, err = s.db.Query(
			`SELECT timestamp, session, session_id, trigger,
			        prompt_preview, response_preview,
			        cost_usd, duration_ms, num_turns, caller_session,
			        turns, rex_user_sends
			 FROM events WHERE timestamp > ?
			 ORDER BY timestamp DESC`, since)
	} else {
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
		rows, err = s.db.Query(
			`SELECT timestamp, session, session_id, trigger,
			        prompt_preview, response_preview,
			        cost_usd, duration_ms, num_turns, caller_session,
			        turns, rex_user_sends
			 FROM events WHERE timestamp >= ?
			 ORDER BY timestamp DESC`, cutoff)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var (
			r            Row
			turns, sends sql.NullString
		)
		if err := rows.Scan(
			&r.Timestamp, &r.Session, &r.SessionID, &r.Trigger,
			&r.PromptPreview, &r.ResponsePreview,
			&r.CostUSD, &r.DurationMS, &r.NumTurns, &r.CallerSession,
			&turns, &sends,
		); err != nil {
			return nil, err
		}
		r.Turns = decodeJSON(turns)
		r.RexUserSends = decodeJSON(sends)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Stats are computed via indexed aggregations — O(1) regardless of history size.
type Stats struct {
	Total    int
	Sessions int
	Cost     float64
	Oldest   string
	Newest   string
}

func (s *Store) Stats() (Stats, error) {
	var st Stats
	row := s.db.QueryRow(`
		SELECT
			COUNT(*),
			COUNT(DISTINCT session_id),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(MIN(timestamp), ''),
			COALESCE(MAX(timestamp), '')
		FROM events`)
	if err := row.Scan(&st.Total, &st.Sessions, &st.Cost, &st.Oldest, &st.Newest); err != nil {
		return st, err
	}
	return st, nil
}

// Clear empties the events table. The file itself is preserved (kept small
// by SQLite's autovacuum / explicit VACUUM later).
func (s *Store) Clear() error {
	_, err := s.db.Exec("DELETE FROM events")
	if err != nil {
		return err
	}
	_, _ = s.db.Exec("VACUUM")
	return nil
}

// RebuildAt opens dbPath without enforcing the current schema, then drops
// the events table and reingests from paths. This is what `rex timeline
// rebuild` calls so it works even when Open would have rejected the DB
// with ErrStaleSchema.
func RebuildAt(dbPath string, paths []string) (int, error) {
	s, err := openForRebuild(dbPath)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	return s.Rebuild(paths)
}

// Rebuild drops the events table, recreates it with the current schema, and
// bulk-inserts every row found in the JSONL files at paths. Returns the
// number of rows inserted. This is the only path that brings a stale DB up
// to the current schema — the user runs `rex timeline rebuild` to reindex.
func (s *Store) Rebuild(paths []string) (int, error) {
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS events`); err != nil {
		return 0, fmt.Errorf("drop events: %w", err)
	}
	if _, err := s.db.Exec(schema); err != nil {
		return 0, fmt.Errorf("apply schema: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.Prepare(`
		INSERT INTO events (
			timestamp, session, session_id, trigger,
			prompt_preview, response_preview,
			cost_usd, duration_ms, num_turns, caller_session,
			turns, rex_user_sends
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	count := 0
	for _, p := range paths {
		f, ferr := os.Open(p)
		if ferr != nil {
			slog.Warn("rebuild skip", "path", p, "err", ferr)
			continue
		}
		n, rerr := ingest(stmt, f)
		f.Close()
		if rerr != nil {
			err = rerr
			return count, err
		}
		count += n
	}
	if err = tx.Commit(); err != nil {
		return count, err
	}
	return count, nil
}

func encodeJSON(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func decodeJSON(ns sql.NullString) any {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(ns.String), &v); err != nil {
		return ns.String // fall back to the raw string on bad JSON
	}
	return v
}
