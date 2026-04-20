// Package events persists agent-turn events to hourly-rotated JSONL files.
// This is the source of truth for the timeline — there is no SQLite mirror.
package events

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event mirrors the on-disk schema. Extra fields round-trip via Extra so
// readers that don't know about newer fields still preserve them.
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
	dir    string // hourly files live here
	legacy string // pre-rotation events.jsonl
	mu     sync.Mutex
}

func NewStore(eventsDir, legacyFile string) *Store {
	return &Store{dir: eventsDir, legacy: legacyFile}
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
	_, err = f.Write(append(data, '\n'))
	return err
}

// Load returns events from the last `days` days, newest first.
func (s *Store) Load(days int) ([]Event, error) {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	return s.loadSince(cutoff)
}

// LoadSince returns events strictly newer than the given RFC3339 timestamp.
// An empty string falls back to a 7-day window.
func (s *Store) LoadSince(since string) ([]Event, error) {
	if since == "" {
		return s.Load(7)
	}
	t, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		t, err = time.Parse(time.RFC3339, since)
		if err != nil {
			return nil, err
		}
	}
	// "strictly newer" mirrors the Python > semantics.
	return s.loadSince(t.Add(time.Nanosecond))
}

func (s *Store) loadSince(cutoff time.Time) ([]Event, error) {
	cutoffHour := cutoff.Truncate(time.Hour)
	var events []Event

	// Hourly-bucketed files.
	entries, err := os.ReadDir(s.dir)
	if err == nil {
		// Skip files whose filename-encoded hour is older than cutoffHour.
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, "events_") || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			stem := strings.TrimSuffix(strings.TrimPrefix(name, "events_"), ".jsonl")
			if fh, err := time.ParseInLocation("2006-01-02_15", stem, time.Local); err == nil {
				if fh.Before(cutoffHour) {
					continue
				}
			}
			more, err := readFile(filepath.Join(s.dir, name), cutoff)
			if err == nil {
				events = append(events, more...)
			}
		}
	}
	// Legacy pre-rotation file.
	if legacy, err := readFile(s.legacy, cutoff); err == nil {
		events = append(events, legacy...)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp > events[j].Timestamp })
	return events, nil
}

func readFile(path string, cutoff time.Time) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.Timestamp != "" {
			t, err := time.Parse(time.RFC3339Nano, e.Timestamp)
			if err != nil {
				t, err = time.Parse(time.RFC3339, e.Timestamp)
			}
			// If parsing succeeded and the event is older than cutoff, skip.
			// Mirror Python: unparseable timestamps are kept, not dropped.
			if err == nil && t.Before(cutoff) {
				continue
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// Stats summarises everything on disk. Used by `rex timeline stats`.
type Stats struct {
	Total        int
	Sessions     int
	Cost         float64
	Oldest       string
	Newest       string
}

func (s *Store) Stats() (Stats, error) {
	all, err := s.loadSince(time.Time{}) // all-time
	if err != nil {
		return Stats{}, err
	}
	seen := make(map[string]struct{})
	var stats Stats
	for _, e := range all {
		stats.Total++
		stats.Cost += e.CostUSD
		if e.SessionID != "" {
			seen[e.SessionID] = struct{}{}
		}
		if stats.Oldest == "" || e.Timestamp < stats.Oldest {
			stats.Oldest = e.Timestamp
		}
		if e.Timestamp > stats.Newest {
			stats.Newest = e.Timestamp
		}
	}
	stats.Sessions = len(seen)
	return stats, nil
}
