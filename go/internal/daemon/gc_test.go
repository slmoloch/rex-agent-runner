package daemon

import (
	"path/filepath"
	"testing"

	"github.com/slmoloch/rex-agent-runner/internal/callback"
	"github.com/slmoloch/rex-agent-runner/internal/events"
	"github.com/slmoloch/rex-agent-runner/internal/session"
	"github.com/slmoloch/rex-agent-runner/internal/telegram"
	"github.com/slmoloch/rex-agent-runner/internal/timeline"
	"github.com/slmoloch/rex-agent-runner/internal/workspace"
)

// A topic's live session is as long-lived as the main one: gc must leave it
// alone, or the next message in that topic would lose its history.
func TestGCKeepsTopicSessions(t *testing.T) {
	dir := t.TempDir()
	tl, err := timeline.Open(filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatalf("open timeline: %v", err)
	}
	defer tl.Close()

	ws := workspace.New(dir)
	if err := ws.EnsureDirs(); err != nil {
		t.Fatalf("workspace dirs: %v", err)
	}
	evStore := events.NewStore(ws.EventsDir, ws.LegacyEvents, tl)

	d := &Daemon{
		sessions:  session.NewStore(ws.SessionsFile, evStore),
		events:    evStore,
		callbacks: callback.NewStore(ws.CallbacksFile),
		timeline:  tl,
		tg:        telegram.New("token", 5),
	}

	d.sessions.SetMainID("main-session", "")
	d.sessions.Register("main-session", session.Main)
	d.sessions.SetID(session.TopicTarget(11), "topic-session", "")
	d.sessions.Register("topic-session", "Authentication (topic:11)")
	d.sessions.Register("scratch-session", "")

	cleaned := d.gcOnce()

	if len(cleaned) != 1 || cleaned[0] != "scratch-session" {
		t.Fatalf("gc cleaned %v, want only scratch-session", cleaned)
	}
	tracked := d.sessions.Tracked()
	if _, ok := tracked["topic-session"]; !ok {
		t.Fatal("gc collected a live topic session")
	}
	if _, ok := tracked["main-session"]; !ok {
		t.Fatal("gc collected the main session")
	}
}
