// Package callback persists user-defined scheduled / one-shot prompts and
// provides an in-daemon scheduler. The on-disk format matches rex_callback.py
// so Python-era callbacks.json files round-trip unchanged.
package callback

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Callback struct {
	ID        string `json:"-"`
	Prompt    string `json:"prompt"`
	Recurring bool   `json:"recurring"`
	Schedule  string `json:"schedule,omitempty"`  // cron expression, recurring only
	At        string `json:"at,omitempty"`        // "YYYY-MM-DD HH:MM", one-shot
	Session   string `json:"session,omitempty"`   // "main" / "new" / raw session id
	Command   string `json:"command,omitempty"`   // pre-check bash command
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) load() map[string]*Callback {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return map[string]*Callback{}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return map[string]*Callback{}
	}
	out := make(map[string]*Callback, len(raw))
	for id, v := range raw {
		cb := &Callback{ID: id}
		if err := json.Unmarshal(v, cb); err == nil {
			out[id] = cb
		}
	}
	return out
}

func (s *Store) save(m map[string]*Callback) error {
	// Write the same shape as Python: id -> callback, alphabetised by id.
	wrapped := make(map[string]*Callback, len(m))
	for k, v := range m {
		wrapped[k] = v
	}
	data, err := json.MarshalIndent(wrapped, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(s.path, data, 0o644)
}

// List returns every callback sorted by id.
func (s *Store) List() []*Callback {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	out := make([]*Callback, 0, len(m))
	for _, cb := range m {
		out = append(out, cb)
	}
	// Stable order for CLI display.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].ID < out[i].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func (s *Store) Get(id string) (*Callback, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cb, ok := s.load()[id]
	return cb, ok
}

// Add stores a new callback. If id is empty, a short random id is generated.
// Returns the final id or an error when the id is already taken.
func (s *Store) Add(id string, cb *Callback) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	if id == "" {
		id = shortID()
	}
	if _, exists := m[id]; exists {
		return "", fmt.Errorf("callback %q already exists", id)
	}
	cb.ID = id
	m[id] = cb
	if err := s.save(m); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	if _, ok := m[id]; !ok {
		return fmt.Errorf("callback %q not found", id)
	}
	delete(m, id)
	return s.save(m)
}

// ParseAt accepts the same formats as rex_callback.py:
//
//	HH:MM                    - today, or tomorrow if already past
//	YYYY-MM-DD HH:MM         - absolute
//	+Nm                      - N minutes from now
//	+Nh                      - N hours from now
func ParseAt(expr string, now time.Time) (time.Time, error) {
	if strings.HasPrefix(expr, "+") {
		v := expr[1:]
		if strings.HasSuffix(v, "m") {
			n, err := strconv.Atoi(strings.TrimSuffix(v, "m"))
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid +Nm: %s", expr)
			}
			return now.Add(time.Duration(n) * time.Minute), nil
		}
		if strings.HasSuffix(v, "h") {
			n, err := strconv.Atoi(strings.TrimSuffix(v, "h"))
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid +Nh: %s", expr)
			}
			return now.Add(time.Duration(n) * time.Hour), nil
		}
		return time.Time{}, fmt.Errorf("use +Nm or +Nh")
	}
	if strings.Contains(expr, " ") {
		t, err := time.ParseInLocation("2006-01-02 15:04", expr, time.Local)
		if err != nil {
			return time.Time{}, fmt.Errorf("use format 'YYYY-MM-DD HH:MM'")
		}
		return t, nil
	}
	t, err := time.ParseInLocation("15:04", expr, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("use format 'HH:MM'")
	}
	scheduled := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if !scheduled.After(now) {
		scheduled = scheduled.Add(24 * time.Hour)
	}
	return scheduled, nil
}

// AtFormat returns the canonical on-disk string for one-shot callbacks.
func AtFormat(t time.Time) string { return t.Format("2006-01-02 15:04") }

func shortID() string {
	const hex = "0123456789abcdef"
	b := make([]byte, 8)
	for i := range b {
		b[i] = hex[rand.IntN(16)]
	}
	return string(b)
}
