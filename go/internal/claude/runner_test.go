package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

// Reproduces the bug where a long Bash `command` field was getting truncated
// at 500 raw bytes, producing invalid JSON like `{"command":"curl ...`. The
// new path clips per-string, so the wrapper object still parses.
func TestParseLine_ToolInputClippedButStillValidJSON(t *testing.T) {
	longCmd := strings.Repeat("x", maxToolStringChars*2)
	line := mustMarshal(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use",
					"name": "Bash",
					"input": map[string]any{
						"command":     longCmd,
						"description": "ok",
					},
				},
			},
		},
	})

	var s runState
	parseLine(line, &s)

	if len(s.turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(s.turns))
	}
	tr := s.turns[0]
	if tr.Type != "tool" || tr.Name != "Bash" {
		t.Fatalf("want tool/Bash, got %+v", tr)
	}

	// Marshalling the entire turn must round-trip cleanly. This is the core
	// invariant: the DB never sees a half-clipped JSON object.
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("turn does not marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("turn does not round-trip: %v\n%s", err, b)
	}

	in, ok := back["input"].(map[string]any)
	if !ok {
		t.Fatalf("input not an object: %T", back["input"])
	}
	cmd, _ := in["command"].(string)
	if cmd == longCmd {
		t.Fatalf("command was not clipped (len=%d)", len(cmd))
	}
	if !strings.HasSuffix(cmd, "…") {
		t.Fatalf("clipped command should end with ellipsis, got: %q", cmd[len(cmd)-20:])
	}
	if in["description"] != "ok" {
		t.Fatalf("short fields should be preserved unchanged, got %v", in["description"])
	}
}

// Text turns appear in stream order alongside tool turns so the on-disk log
// is a chronological record of what Claude did, not just what tools it ran.
func TestParseLine_AssistantTextAndToolsAreChronological(t *testing.T) {
	line := mustMarshal(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "thinking..."},
				map[string]any{
					"type":  "tool_use",
					"name":  "Read",
					"input": map[string]any{"file_path": "/tmp/x"},
				},
				map[string]any{"type": "text", "text": "done"},
			},
		},
	})

	var s runState
	parseLine(line, &s)

	got := []string{}
	for _, tr := range s.turns {
		if tr.Type == "text" {
			got = append(got, "text:"+tr.Text)
		} else {
			got = append(got, "tool:"+tr.Name)
		}
	}
	want := []string{"text:thinking...", "tool:Read", "text:done"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("turn order: got %v, want %v", got, want)
	}
}

// Long assistant text turns are clipped to a fixed cap so a single chatty
// turn can't blow up event sizes.
func TestParseLine_TextTurnIsClipped(t *testing.T) {
	long := strings.Repeat("y", maxTextTurnChars*2)
	line := mustMarshal(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": long},
			},
		},
	})

	var s runState
	parseLine(line, &s)
	if len(s.turns) != 1 || s.turns[0].Type != "text" {
		t.Fatalf("want 1 text turn, got %+v", s.turns)
	}
	if s.turns[0].Text == long {
		t.Fatalf("text was not clipped")
	}
	if !strings.HasSuffix(s.turns[0].Text, "…") {
		t.Fatalf("clipped text should end with ellipsis")
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
