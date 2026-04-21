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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; registers as "sqlite"
)

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
    tools TEXT,
    rex_user_sends TEXT
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_sid ON events(session_id);
`

// Row mirrors the events table. Tools/RexUserSends are stored as JSON text
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
	Tools           any     `json:"tools,omitempty"`
	RexUserSends    any     `json:"rex_user_sends,omitempty"`
}

type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database and applies the schema.
func Open(dbPath string) (*Store, error) {
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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Insert appends one event row.
func (s *Store) Insert(r Row) error {
	tools, err := encodeJSON(r.Tools)
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
			tools, rex_user_sends
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Timestamp, r.Session, r.SessionID, r.Trigger,
		r.PromptPreview, r.ResponsePreview,
		r.CostUSD, r.DurationMS, r.NumTurns, r.CallerSession,
		tools, sends,
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
			        tools, rex_user_sends
			 FROM events WHERE timestamp > ?
			 ORDER BY timestamp DESC`, since)
	} else {
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
		rows, err = s.db.Query(
			`SELECT timestamp, session, session_id, trigger,
			        prompt_preview, response_preview,
			        cost_usd, duration_ms, num_turns, caller_session,
			        tools, rex_user_sends
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
			r              Row
			tools, sends   sql.NullString
		)
		if err := rows.Scan(
			&r.Timestamp, &r.Session, &r.SessionID, &r.Trigger,
			&r.PromptPreview, &r.ResponsePreview,
			&r.CostUSD, &r.DurationMS, &r.NumTurns, &r.CallerSession,
			&tools, &sends,
		); err != nil {
			return nil, err
		}
		r.Tools = decodeJSON(tools)
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

// Rebuild clears the DB and bulk-inserts every row found in the JSONL files
// at paths. Returns the number of rows inserted.
func (s *Store) Rebuild(paths []string) (int, error) {
	if err := s.Clear(); err != nil {
		return 0, err
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
			tools, rex_user_sends
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
