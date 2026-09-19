package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/slmoloch/rex-agent-runner/internal/session"
	"github.com/slmoloch/rex-agent-runner/internal/telegram"
)

func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{
		sessions: session.NewStore(filepath.Join(t.TempDir(), "sessions.json"), nil),
		tg:       telegram.New("token", 5),
	}
}

func topicMessage(chatID, threadID int64, text, topicName string) *telegram.Message {
	m := &telegram.Message{
		From:            &telegram.User{ID: 5},
		Chat:            telegram.Chat{ID: chatID, Type: "supergroup", IsForum: true},
		MessageThreadID: threadID,
		IsTopicMessage:  true,
		Text:            text,
	}
	if topicName != "" {
		m.ReplyToMessage = &telegram.Message{
			ForumTopicCreated: &telegram.ForumTopicCreated{Name: topicName},
		}
	}
	return m
}

// Two topics in one supergroup must resolve to two different sessions, and a
// DM must still resolve to main.
func TestOriginForSeparatesTopics(t *testing.T) {
	d := newTestDaemon(t)

	auth := d.originFor(topicMessage(-100123, 11, "auth is broken", "Authentication"))
	db := d.originFor(topicMessage(-100123, 22, "slow queries", "Database performance"))
	dm := d.originFor(&telegram.Message{
		From: &telegram.User{ID: 5},
		Chat: telegram.Chat{ID: 5, Type: "private"},
		Text: "hi",
	})

	if auth.target != "topic:11" || db.target != "topic:22" {
		t.Fatalf("topic targets = %q / %q, want topic:11 / topic:22", auth.target, db.target)
	}
	if auth.target == db.target {
		t.Fatal("two topics share a session target")
	}
	if dm.target != session.Main {
		t.Fatalf("DM target = %q, want main", dm.target)
	}
	if auth.tg.ThreadID != 11 || db.tg.ThreadID != 22 || dm.tg.ThreadID != 0 {
		t.Fatalf("reply clients bound to threads %d / %d / %d",
			auth.tg.ThreadID, db.tg.ThreadID, dm.tg.ThreadID)
	}
	if auth.tg.ChatID != -100123 {
		t.Fatalf("topic reply client chat = %d, want -100123", auth.tg.ChatID)
	}
	if dm.tg.ChatID != 5 {
		t.Fatalf("DM reply client chat = %d, want 5", dm.tg.ChatID)
	}

	// The binding is persisted, title included, so out-of-band replies can
	// find the topic again.
	topic, ok := d.sessions.TopicFor("topic:11")
	if !ok {
		t.Fatal("topic binding not recorded")
	}
	if topic.Name != "Authentication" || topic.ChatID != -100123 || topic.ThreadID != 11 {
		t.Fatalf("topic binding = %+v", topic)
	}
}

// Sessions started in a topic must not leak into the main session or into a
// sibling topic.
func TestTopicSessionsDoNotLeak(t *testing.T) {
	d := newTestDaemon(t)
	auth := d.originFor(topicMessage(-100123, 11, "auth", "Authentication"))
	db := d.originFor(topicMessage(-100123, 22, "db", "Database performance"))

	d.sessions.SetID(auth.target, "session-a", "")
	d.sessions.SetID(db.target, "session-b", "")

	if got := d.sessions.Resolve(auth.target); got != "session-a" {
		t.Fatalf("auth session = %q, want session-a", got)
	}
	if got := d.sessions.Resolve(db.target); got != "session-b" {
		t.Fatalf("db session = %q, want session-b", got)
	}
	if got := d.sessions.GetMainID(); got != "" {
		t.Fatalf("main session = %q, want empty", got)
	}
}

func TestReplyEnvFromOrigin(t *testing.T) {
	d := newTestDaemon(t)
	o := d.originFor(topicMessage(-100123, 11, "auth", "Authentication"))

	env := d.replyEnv(o.target, "", "", &o)
	assertEnv(t, env, "REX_TELEGRAM_CHAT_ID=-100123", "REX_TELEGRAM_TOPIC_ID=11")
}

// A callback or dispatch has no incoming message; the topic is recovered
// from the stored binding, by target or by the session id it is serving.
func TestReplyEnvWithoutOrigin(t *testing.T) {
	d := newTestDaemon(t)
	o := d.originFor(topicMessage(-100123, 11, "auth", "Authentication"))
	d.sessions.SetID(o.target, "session-a", "")

	byTarget := d.replyEnv(o.target, "session-a", "", nil)
	assertEnv(t, byTarget, "REX_TELEGRAM_CHAT_ID=-100123", "REX_TELEGRAM_TOPIC_ID=11")

	bySessionID := d.replyEnv("session-a", "session-a", "", nil)
	assertEnv(t, bySessionID, "REX_TELEGRAM_CHAT_ID=-100123", "REX_TELEGRAM_TOPIC_ID=11")

	// `rex dispatch new` from inside a topic: the spawned session reports
	// back into the topic that started the work.
	spawned := d.replyEnv("new", "", "session-a", nil)
	assertEnv(t, spawned, "REX_TELEGRAM_CHAT_ID=-100123", "REX_TELEGRAM_TOPIC_ID=11")

	if env := d.replyEnv(session.Main, "", "", nil); len(env) != 0 {
		t.Fatalf("main session env = %v, want none", env)
	}
}

func TestSessionLabel(t *testing.T) {
	d := newTestDaemon(t)
	o := d.originFor(topicMessage(-100123, 11, "auth", "Authentication"))

	if got := d.sessionLabel(session.Main); got != session.Main {
		t.Fatalf("main label = %q, want main", got)
	}
	if got := d.sessionLabel(o.target); got != "Authentication (topic:11)" {
		t.Fatalf("topic label = %q", got)
	}
	unnamed := d.originFor(topicMessage(-100123, 33, "no title yet", ""))
	if got := d.sessionLabel(unnamed.target); got != "topic:33" {
		t.Fatalf("unnamed topic label = %q, want topic:33", got)
	}
	if got := d.sessionLabel("new"); got != "" {
		t.Fatalf("ad-hoc session label = %q, want empty", got)
	}
}

// The topic preamble is attached when a topic session starts, and dropped
// once that session exists.
func TestWithTopicContext(t *testing.T) {
	d := newTestDaemon(t)
	o := d.originFor(topicMessage(-100123, 11, "auth is broken", "Authentication"))

	first := d.withTopicContext("auth is broken", o)
	if first == "auth is broken" {
		t.Fatal("first prompt in a topic should carry the topic context")
	}
	if !strings.Contains(first, `"Authentication"`) || !strings.Contains(first, "topic:11") {
		t.Fatalf("topic context missing details: %q", first)
	}

	d.sessions.SetID(o.target, "session-a", "")
	if got := d.withTopicContext("what about now", o); got != "what about now" {
		t.Fatalf("resumed topic prompt = %q, want it unchanged", got)
	}

	dm := d.originFor(&telegram.Message{
		From: &telegram.User{ID: 5},
		Chat: telegram.Chat{ID: 5, Type: "private"},
		Text: "hi",
	})
	if got := d.withTopicContext("hi", dm); got != "hi" {
		t.Fatalf("DM prompt = %q, want it unchanged", got)
	}
}

func assertEnv(t *testing.T, env []string, want ...string) {
	t.Helper()
	if len(env) != len(want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
	for i, w := range want {
		if env[i] != w {
			t.Fatalf("env = %v, want %v", env, want)
		}
	}
}
