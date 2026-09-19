// Package session tracks the session id behind every conversation rex holds
// — the main one, one per Telegram forum topic — and registers any session
// that has seen activity so the gc loop can reason about them.
package session

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/events"
)

const Main = "main"

// TopicPrefix marks a session target bound to a Telegram forum topic:
// "topic:<message_thread_id>". Each topic keeps its own Claude session, so
// a conversation in one topic never bleeds into another.
const TopicPrefix = "topic:"

// TopicTarget builds the session target for a forum topic thread id.
func TopicTarget(threadID int64) string {
	return TopicPrefix + strconv.FormatInt(threadID, 10)
}

// IsTopicTarget reports whether target names a forum topic session.
func IsTopicTarget(target string) bool {
	return strings.HasPrefix(target, TopicPrefix)
}

// ParseTopicTarget extracts the thread id from a "topic:<id>" target.
func ParseTopicTarget(target string) (int64, bool) {
	if !IsTopicTarget(target) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(target, TopicPrefix), 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

type Tracked struct {
	Name         string `json:"name,omitempty"`
	LastActivity string `json:"last_activity"`
}

// Topic is the persisted binding between a Telegram forum topic and the
// Claude session serving it. ChatID / ThreadID are kept so replies produced
// outside a Telegram update (scheduled callbacks, dispatches, `rex user`)
// can still be routed back into the right topic.
type Topic struct {
	SessionID    string `json:"session_id,omitempty"`
	Name         string `json:"name,omitempty"`
	ChatID       int64  `json:"chat_id,omitempty"`
	ThreadID     int64  `json:"thread_id,omitempty"`
	LastActivity string `json:"last_activity,omitempty"`
}

// file is the on-disk JSON shape of sessions.json.
type file struct {
	Main    *string            `json:"main,omitempty"`
	Topics  map[string]Topic   `json:"topics,omitempty"`
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
	if v, ok := raw["topics"]; ok {
		_ = json.Unmarshal(v, &f.Topics)
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

// GetID returns the live Claude session id behind a conversation target
// ("main" or "topic:<id>"), or "" when none has been started yet.
func (s *Store) GetID(target string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	return idOf(f, target)
}

func idOf(f file, target string) string {
	if IsTopicTarget(target) {
		return f.Topics[target].SessionID
	}
	if target == Main && f.Main != nil {
		return *f.Main
	}
	return ""
}

// GetMainID returns the active main session id, or "" if unset.
func (s *Store) GetMainID() string { return s.GetID(Main) }

// SetID stores a new session id for a conversation target — but only if the
// current id is still `expected`. This prevents a long-running turn from
// resurrecting a session that was reset meanwhile. Passing expected=""
// means "set unconditionally".
func (s *Store) SetID(target, newID, expected string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	if expected != "" && idOf(f, target) != expected {
		return
	}
	switch {
	case IsTopicTarget(target):
		if f.Topics == nil {
			f.Topics = make(map[string]Topic)
		}
		t := f.Topics[target]
		t.SessionID = newID
		t.LastActivity = nowStamp()
		if t.ThreadID == 0 {
			if id, ok := ParseTopicTarget(target); ok {
				t.ThreadID = id
			}
		}
		f.Topics[target] = t
	case target == Main:
		f.Main = &newID
	default:
		return // raw session ids and "new" have nothing to persist
	}
	_ = s.save(f)
}

// SetMainID stores a new main session id, guarded by `expected` as in SetID.
func (s *Store) SetMainID(newID, expected string) { s.SetID(Main, newID, expected) }

// Reset clears the session behind a target and, if there was one, emits a
// reset event. The topic binding itself (name, chat, thread) is kept so the
// next message in that topic lands in a fresh session for the same topic.
func (s *Store) Reset(target string) {
	s.mu.Lock()
	f := s.load()
	old := idOf(f, target)
	switch {
	case IsTopicTarget(target):
		if t, ok := f.Topics[target]; ok {
			t.SessionID = ""
			f.Topics[target] = t
		}
	case target == Main:
		f.Main = nil
	default:
		s.mu.Unlock()
		return
	}
	_ = s.save(f)
	s.mu.Unlock()

	if old != "" && s.events != nil {
		label := "Main session reset"
		if IsTopicTarget(target) {
			label = "Topic session reset"
		}
		_ = s.events.Append(events.Event{
			Session:       target,
			SessionID:     old,
			Trigger:       "reset",
			PromptPreview: label,
		})
	}
}

// ResetMain clears the main session and, if there was one, emits a reset event.
func (s *Store) ResetMain() { s.Reset(Main) }

// Resolve turns a user-facing target into a Claude session id.
//
//	"new"        -> "" (start fresh)
//	"main"       -> stored main id (or "")
//	"topic:<id>" -> that topic's stored id (or "")
//	else         -> the literal id
func (s *Store) Resolve(target string) string {
	switch {
	case target == "new":
		return ""
	case target == Main || IsTopicTarget(target):
		return s.GetID(target)
	default:
		return target
	}
}

// TouchTopic records (or refreshes) the binding between a forum topic and
// the chat it lives in. Name is only written when known — the Bot API
// exposes topic titles on service messages only, so an empty name never
// overwrites one we learned earlier.
func (s *Store) TouchTopic(target string, chatID, threadID int64, name string) {
	if !IsTopicTarget(target) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	if f.Topics == nil {
		f.Topics = make(map[string]Topic)
	}
	t := f.Topics[target]
	if chatID != 0 {
		t.ChatID = chatID
	}
	if threadID != 0 {
		t.ThreadID = threadID
	}
	if name != "" {
		t.Name = name
	}
	t.LastActivity = nowStamp()
	f.Topics[target] = t
	_ = s.save(f)
}

// Topics returns a copy of every known forum-topic binding, keyed by target.
func (s *Store) Topics() map[string]Topic {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	out := make(map[string]Topic, len(f.Topics))
	for k, v := range f.Topics {
		out[k] = v
	}
	return out
}

// TopicFor returns the binding for a target, or a zero Topic when unknown.
func (s *Store) TopicFor(target string) (Topic, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	t, ok := f.Topics[target]
	return t, ok
}

// TopicForSessionID finds the topic currently served by a Claude session id.
// Used to route out-of-band replies (callbacks, dispatches) back into the
// topic the session belongs to.
func (s *Store) TopicForSessionID(sessionID string) (string, Topic, bool) {
	if sessionID == "" {
		return "", Topic{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.load()
	for target, t := range f.Topics {
		if t.SessionID == sessionID {
			return target, t, true
		}
	}
	return "", Topic{}, false
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
	now := nowStamp()
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

func nowStamp() string { return time.Now().Format(time.RFC3339Nano) }
