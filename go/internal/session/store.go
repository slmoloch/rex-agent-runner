// Package session tracks the main session ID and registers any session that
// has seen activity so the gc loop can reason about them.
package session

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/events"
)

const Main = "main"

type Tracked struct {
	Name         string `json:"name,omitempty"`
	LastActivity string `json:"last_activity"`
}

// file is the on-disk JSON shape of sessions.json.
type file struct {
	Main    *string            `json:"main,omitempty"`
	Tracked map[string]Tracked `json:"tracked,omitempty"`
}

type Store struct {
	path string
	mu   sync.Mutex

	events  *events.Store // for emitting the "reset" event
	running sync.Map      // set[session_id] of in-flight turns
}

func NewStore(path string, ev *events.Store) *Store {
	return &Store{path: path, events: ev}
}

func (s *Store) load() file {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return file{}
	}
	// Accept both the legacy "main" string and the newer tracked map.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return file{}
	}
	var f file
	if v, ok := raw["main"]; ok && string(v) != "null" {
		var s string
		if json.Unmarshal(v, &s) == nil {
			f.Main = &s
		}
	}
	if v, ok := raw["tracked"]; ok {
		_ = json.Unmarshal(v, &f.Tracked)
	}
	return f
}

func (s *Store) save(f file) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(s.path, data, 0o644)
}

// GetMainID returns the active main session id, or "" if unset.
func (s *Store) GetMainID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	if f.Main != nil {
		return *f.Main
	}
	return ""
}

// SetMainID stores a new main session id — but only if the current id is
// still `expected`. This prevents a long-running turn from resurrecting a
// reset session. Passing expected="" means "set unconditionally".
func (s *Store) SetMainID(newID, expected string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	if expected != "" {
		cur := ""
		if f.Main != nil {
			cur = *f.Main
		}
		if cur != expected {
			return
		}
	}
	f.Main = &newID
	_ = s.save(f)
}

// ResetMain clears the main session and, if there was one, emits a reset event.
func (s *Store) ResetMain() {
	s.mu.Lock()
	f := s.load()
	old := ""
	if f.Main != nil {
		old = *f.Main
	}
	f.Main = nil
	_ = s.save(f)
	s.mu.Unlock()

	if old != "" && s.events != nil {
		_ = s.events.Append(events.Event{
			Session:         Main,
			SessionID:       old,
			Trigger:         "reset",
			PromptPreview:   "Main session reset",
		})
	}
}

// Resolve turns a user-facing target into a Claude session id.
//
//	"new"  -> "" (start fresh)
//	"main" -> stored main id (or "")
//	else   -> the literal id
func (s *Store) Resolve(target string) string {
	switch target {
	case "new":
		return ""
	case Main:
		return s.GetMainID()
	default:
		return target
	}
}

// Register records that a session is alive. Updates last_activity and
// (optionally) the first-seen name.
func (s *Store) Register(sessionID, name string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	if f.Tracked == nil {
		f.Tracked = make(map[string]Tracked)
	}
	now := time.Now().Format(time.RFC3339Nano)
	if t, ok := f.Tracked[sessionID]; ok {
		t.LastActivity = now
		if name != "" && t.Name == "" {
			t.Name = name
		}
		f.Tracked[sessionID] = t
	} else {
		f.Tracked[sessionID] = Tracked{Name: name, LastActivity: now}
	}
	_ = s.save(f)
}

func (s *Store) Unregister(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	if _, ok := f.Tracked[sessionID]; ok {
		delete(f.Tracked, sessionID)
		_ = s.save(f)
	}
}

func (s *Store) Tracked() map[string]Tracked {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	out := make(map[string]Tracked, len(f.Tracked))
	for k, v := range f.Tracked {
		out[k] = v
	}
	return out
}

// MarkRunning / MarkStopped / IsRunning track in-flight turns. The set is
// in-memory — when the daemon restarts, every session is considered idle
// until something re-marks it.
func (s *Store) MarkRunning(sessionID string) {
	if sessionID != "" {
		s.running.Store(sessionID, struct{}{})
	}
}
func (s *Store) MarkStopped(sessionID string) {
	s.running.Delete(sessionID)
}
func (s *Store) IsRunning(sessionID string) bool {
	_, ok := s.running.Load(sessionID)
	return ok
}
