package daemon

import (
	"reflect"
	"strings"
	"testing"

	"github.com/slmoloch/rex-agent-runner/internal/session"
)

// Every topic talked in since the last sweep holds a live session id, and
// those are exactly the ones whose history is about to be discarded.
func TestTopicsToSweep(t *testing.T) {
	topics := map[string]session.Topic{
		"topic:11": {SessionID: "sess-auth", Name: "Authentication"},
		"topic:22": {SessionID: "sess-db", Name: "Database performance"},
		"topic:33": {SessionID: "", Name: "Untouched since the last reset"},
		"topic:44": {SessionID: "sess-busy", Name: "Mid-turn"},
	}
	running := map[string]bool{"sess-busy": true}

	sweep, busy := topicsToSweep(topics, func(id string) bool { return running[id] })

	if want := []string{"topic:11", "topic:22"}; !reflect.DeepEqual(sweep, want) {
		t.Fatalf("sweep = %v, want %v", sweep, want)
	}
	if want := []string{"topic:44"}; !reflect.DeepEqual(busy, want) {
		t.Fatalf("busy = %v, want %v", busy, want)
	}
}

// The order must not depend on map iteration: the sweeps run one after
// another and all write the same MEMORY.md.
func TestTopicsToSweepIsOrdered(t *testing.T) {
	topics := map[string]session.Topic{}
	for _, target := range []string{"topic:9", "topic:11", "topic:22", "topic:7"} {
		topics[target] = session.Topic{SessionID: "sess" + target}
	}
	idle := func(string) bool { return false }

	first, _ := topicsToSweep(topics, idle)
	for i := 0; i < 5; i++ {
		again, _ := topicsToSweep(topics, idle)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("sweep order not stable: %v vs %v", first, again)
		}
	}
	if want := []string{"topic:11", "topic:22", "topic:7", "topic:9"}; !reflect.DeepEqual(first, want) {
		t.Fatalf("sweep = %v, want %v", first, want)
	}
}

func TestTopicsToSweepWithNoTopics(t *testing.T) {
	sweep, busy := topicsToSweep(map[string]session.Topic{}, func(string) bool { return false })
	if len(sweep) != 0 || len(busy) != 0 {
		t.Fatalf("sweep=%v busy=%v, want both empty", sweep, busy)
	}
}

func TestPrepareResetPromptAsksForTheMemorySweep(t *testing.T) {
	main := PrepareResetPrompt(session.Main, "")
	for _, want := range []string{"MEMORY.md", "about to be reset", "Don't message the user"} {
		if !strings.Contains(main, want) {
			t.Fatalf("main sweep prompt missing %q: %s", want, main)
		}
	}
	if strings.Contains(main, "forum topic") {
		t.Fatalf("main sweep prompt should not mention a topic: %s", main)
	}
}

// A topic's sweep has to say which topic it covers, or the memories it
// writes read as if everything happened in one conversation.
func TestPrepareResetPromptNamesTheTopic(t *testing.T) {
	named := PrepareResetPrompt("topic:11", "Authentication")
	for _, want := range []string{"MEMORY.md", `"Authentication"`, "topic:11", "Attribute"} {
		if !strings.Contains(named, want) {
			t.Fatalf("topic sweep prompt missing %q: %s", want, named)
		}
	}

	// Topics created before the bot joined have no title; the target still
	// identifies them.
	unnamed := PrepareResetPrompt("topic:33", "")
	if !strings.Contains(unnamed, "topic:33") || strings.Contains(unnamed, `""`) {
		t.Fatalf("unnamed topic sweep prompt = %s", unnamed)
	}
}
