// Package events is the append path for agent-turn events. Each event goes
// to both an hourly-rotated JSONL file (the audit log, retention-eligible)
// and — when configured — the timeline SQLite database (permanent, queryable).
//
// The JSONL files are authoritative for rebuilding the SQLite DB after loss
// or corruption (see timeline.Store.Rebuild).
package events

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/timeline"
)

// Event mirrors the on-disk JSONL shape.
type Event struct {
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
	dir      string
	legacy   string
	timeline *timeline.Store // optional; nil means "JSONL only"
	mu       sync.Mutex
}

// NewStore creates a writer backed by hourly JSONL files. Passing a non-nil
// timeline enables the SQLite mirror.
func NewStore(eventsDir, legacyFile string, tl *timeline.Store) *Store {
	return &Store{dir: eventsDir, legacy: legacyFile, timeline: tl}
}

// Append writes one event. Timestamp is filled if missing.
func (s *Store) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Timestamp == "" {
		e.Timestamp = time.Now().Format(time.RFC3339Nano)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.dir, time.Now().Format("events_2006-01-02_15")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}

	// Mirror into SQLite. Failure here is non-fatal — JSONL is authoritative
	// and the DB can be rebuilt from it.
	if s.timeline != nil {
		if err := s.timeline.Insert(timelineRow(e)); err != nil {
			slog.Warn("timeline insert failed (jsonl still authoritative)", "err", err)
		}
	}
	return nil
}

func timelineRow(e Event) timeline.Row {
	return timeline.Row{
		Timestamp:       e.Timestamp,
		Session:         e.Session,
		SessionID:       e.SessionID,
		Trigger:         e.Trigger,
		PromptPreview:   e.PromptPreview,
		ResponsePreview: e.ResponsePreview,
		CostUSD:         e.CostUSD,
		DurationMS:      e.DurationMS,
		NumTurns:        e.NumTurns,
		CallerSession:   e.CallerSession,
		Tools:           e.Tools,
		RexUserSends:    e.RexUserSends,
	}
}

// JSONLFiles returns every JSONL path that contributes to the audit log,
// sorted by filename (which is equivalent to chronological for hourly
// rotation). Used by `rex timeline rebuild`.
func JSONLFiles(eventsDir, legacyFile string) []string {
	var out []string
	if entries, err := os.ReadDir(eventsDir); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.HasPrefix(n, "events_") && strings.HasSuffix(n, ".jsonl") {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, filepath.Join(eventsDir, n))
		}
	}
	if _, err := os.Stat(legacyFile); err == nil {
		out = append(out, legacyFile)
	}
	return out
}

// CleanupOlderThan removes hourly JSONL files whose hour-of-name is older
// than age. SQLite is untouched. Returns the deleted paths.
func CleanupOlderThan(eventsDir string, age time.Duration) []string {
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-age).Truncate(time.Hour)
	var deleted []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasPrefix(n, "events_") || !strings.HasSuffix(n, ".jsonl") {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(n, "events_"), ".jsonl")
		t, err := time.ParseInLocation("2006-01-02_15", stem, time.Local)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			p := filepath.Join(eventsDir, n)
			if err := os.Remove(p); err == nil {
				deleted = append(deleted, p)
			}
		}
	}
	return deleted
}
