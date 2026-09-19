package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "sessions.json"), nil)
}

func TestTopicTargetRoundTrip(t *testing.T) {
	target := TopicTarget(77)
	if target != "topic:77" {
		t.Fatalf("TopicTarget(77) = %q, want topic:77", target)
	}
	if !IsTopicTarget(target) {
		t.Fatal("topic:77 should be a topic target")
	}
	if id, ok := ParseTopicTarget(target); !ok || id != 77 {
		t.Fatalf("ParseTopicTarget = (%d, %v), want (77, true)", id, ok)
	}
	for _, bad := range []string{"main", "new", "topic:", "topic:abc", "topic:0", "4f3a-uuid"} {
		if _, ok := ParseTopicTarget(bad); ok {
			t.Fatalf("ParseTopicTarget(%q) unexpectedly succeeded", bad)
		}
	}
}

// Each topic keeps its own session: writing one never disturbs another, and
// none of them touch main.
func TestTopicSessionsAreIndependent(t *testing.T) {
	s := newTestStore(t)
	auth, db := TopicTarget(11), TopicTarget(22)

	s.SetMainID("main-session", "")
	s.SetID(auth, "session-a", "")
	s.SetID(db, "session-b", "")

	if got := s.GetID(auth); got != "session-a" {
		t.Fatalf("auth topic session = %q, want session-a", got)
	}
	if got := s.GetID(db); got != "session-b" {
		t.Fatalf("db topic session = %q, want session-b", got)
	}
	if got := s.GetMainID(); got != "main-session" {
		t.Fatalf("main session = %q, want main-session", got)
	}
	if got := s.Resolve(auth); got != "session-a" {
		t.Fatalf("Resolve(%s) = %q, want session-a", auth, got)
	}
	if got := s.Resolve("new"); got != "" {
		t.Fatalf("Resolve(new) = %q, want empty", got)
	}
	if got := s.Resolve("raw-id"); got != "raw-id" {
		t.Fatalf("Resolve(raw-id) = %q, want raw-id", got)
	}

	// Resetting one topic leaves the others (and main) alone.
	s.Reset(auth)
	if got := s.GetID(auth); got != "" {
		t.Fatalf("auth topic session after reset = %q, want empty", got)
	}
	if got := s.GetID(db); got != "session-b" {
		t.Fatalf("db topic session after resetting auth = %q, want session-b", got)
	}
	if got := s.GetMainID(); got != "main-session" {
		t.Fatalf("main session after resetting a topic = %q, want main-session", got)
	}
}

func TestResetKeepsTopicBinding(t *testing.T) {
	s := newTestStore(t)
	target := TopicTarget(11)
	s.TouchTopic(target, -100123, 11, "Authentication")
	s.SetID(target, "session-a", "")

	s.Reset(target)

	topic, ok := s.TopicFor(target)
	if !ok {
		t.Fatal("topic binding disappeared on reset")
	}
	if topic.SessionID != "" {
		t.Fatalf("session id survived reset: %q", topic.SessionID)
	}
	if topic.Name != "Authentication" || topic.ChatID != -100123 || topic.ThreadID != 11 {
		t.Fatalf("binding lost metadata: %+v", topic)
	}
}

// A turn that finishes after its topic was reset must not resurrect the old
// session id.
func TestSetIDGuardsAgainstStaleWrites(t *testing.T) {
	s := newTestStore(t)
	target := TopicTarget(11)
	s.SetID(target, "session-a", "")
	s.Reset(target)

	s.SetID(target, "session-a-continued", "session-a")
	if got := s.GetID(target); got != "" {
		t.Fatalf("stale write resurrected the session: %q", got)
	}

	s.SetID(target, "session-c", "")
	if got := s.GetID(target); got != "session-c" {
		t.Fatalf("unconditional write = %q, want session-c", got)
	}
}

func TestTouchTopicKeepsKnownName(t *testing.T) {
	s := newTestStore(t)
	target := TopicTarget(11)
	s.TouchTopic(target, -100123, 11, "Authentication")
	s.TouchTopic(target, -100123, 11, "") // a later message carries no title

	topic, _ := s.TopicFor(target)
	if topic.Name != "Authentication" {
		t.Fatalf("topic name = %q, want Authentication", topic.Name)
	}
	if topic.LastActivity == "" {
		t.Fatal("last activity not recorded")
	}
}

func TestTopicForSessionID(t *testing.T) {
	s := newTestStore(t)
	target := TopicTarget(11)
	s.TouchTopic(target, -100123, 11, "Authentication")
	s.SetID(target, "session-a", "")

	gotTarget, topic, ok := s.TopicForSessionID("session-a")
	if !ok || gotTarget != target || topic.ChatID != -100123 || topic.ThreadID != 11 {
		t.Fatalf("TopicForSessionID = (%q, %+v, %v)", gotTarget, topic, ok)
	}
	if _, _, ok := s.TopicForSessionID("unknown"); ok {
		t.Fatal("unknown session id should not match a topic")
	}
	if _, _, ok := s.TopicForSessionID(""); ok {
		t.Fatal("empty session id should not match a topic")
	}
}

// sessions.json written before topics existed must keep working.
func TestLegacyFileStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	legacy := `{"main":"main-session","tracked":{"main-session":{"name":"main","last_activity":"2026-01-01T00:00:00Z"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path, nil)

	if got := s.GetMainID(); got != "main-session" {
		t.Fatalf("legacy main = %q, want main-session", got)
	}
	if got := len(s.Tracked()); got != 1 {
		t.Fatalf("legacy tracked count = %d, want 1", got)
	}
	if got := len(s.Topics()); got != 0 {
		t.Fatalf("legacy topics = %d, want 0", got)
	}

	// Adding a topic preserves everything already in the file.
	s.SetID(TopicTarget(11), "session-a", "")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Main    string                     `json:"main"`
		Topics  map[string]json.RawMessage `json:"topics"`
		Tracked map[string]json.RawMessage `json:"tracked"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("rewritten file does not parse: %v", err)
	}
	if f.Main != "main-session" || len(f.Tracked) != 1 || len(f.Topics) != 1 {
		t.Fatalf("rewritten file = %s", data)
	}
}
