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

// Thinking blocks are captured as their own turn type so the timeline can
// show what the agent was reasoning about, not just what it ran. They share
// the text clip cap because the intent is a record of activity, not a full
// trace dump.
func TestParseLine_ThinkingTurnIsCapturedAndClipped(t *testing.T) {
	long := strings.Repeat("z", maxTextTurnChars*2)
	line := mustMarshal(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "short reasoning"},
				map[string]any{"type": "text", "text": "answer"},
				map[string]any{"type": "thinking", "thinking": long},
			},
		},
	})

	var s runState
	parseLine(line, &s)

	if len(s.turns) != 3 {
		t.Fatalf("want 3 turns, got %d: %+v", len(s.turns), s.turns)
	}
	if s.turns[0].Type != "thinking" || s.turns[0].Text != "short reasoning" {
		t.Fatalf("turn[0] = %+v", s.turns[0])
	}
	if s.turns[1].Type != "text" || s.turns[1].Text != "answer" {
		t.Fatalf("turn[1] = %+v", s.turns[1])
	}
	if s.turns[2].Type != "thinking" {
		t.Fatalf("turn[2] not thinking: %+v", s.turns[2])
	}
	if s.turns[2].Text == long {
		t.Fatalf("long thinking was not clipped")
	}
	if !strings.HasSuffix(s.turns[2].Text, "…") {
		t.Fatalf("clipped thinking should end with ellipsis")
	}
}

// User events carry tool_result content blocks. They become Turn entries
// with type=="tool_result", paired to the originating tool_use by
// ToolUseID. Long outputs are clipped the same way text is.
func TestParseLine_UserToolResultIsCaptured(t *testing.T) {
	long := strings.Repeat("o", maxToolResultChars*2)
	line := mustMarshal(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_123",
					"content":     long,
					"is_error":    false,
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
	if tr.Type != "tool_result" || tr.ToolUseID != "toolu_123" {
		t.Fatalf("bad turn: %+v", tr)
	}
	out, ok := tr.Output.(string)
	if !ok {
		t.Fatalf("output should be a clipped string, got %T", tr.Output)
	}
	if out == long {
		t.Fatalf("tool_result content was not clipped")
	}
	if !strings.HasSuffix(out, "…") {
		t.Fatalf("clipped output should end with ellipsis")
	}
}

// Some tools (Read on an image, etc) return a list of content blocks
// instead of a bare string. Block-array shape must survive parsing with
// string leaves clipped, not turned into a stringified blob.
func TestParseLine_UserToolResultBlockArrayShape(t *testing.T) {
	line := mustMarshal(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_img",
					"content": []any{
						map[string]any{"type": "text", "text": "summary"},
						map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":       "base64",
								"media_type": "image/png",
								"data":       "ABCDEFG",
							},
						},
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
	if _, ok := s.turns[0].Output.([]any); !ok {
		t.Fatalf("multimodal output should remain a slice, got %T", s.turns[0].Output)
	}
}

// Errored tool results flow through with IsError=true so downstream layers
// (the timeline UI, debugging tools) can flag them.
func TestParseLine_UserToolResultErrorFlagPropagates(t *testing.T) {
	line := mustMarshal(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_bad",
					"content":     "ENOENT",
					"is_error":    true,
				},
			},
		},
	})

	var s runState
	parseLine(line, &s)
	if len(s.turns) != 1 || !s.turns[0].IsError {
		t.Fatalf("is_error should propagate, got: %+v", s.turns)
	}
}

// User events that aren't tool results (injected user messages mid-run)
// should not produce turns — the run log is a record of agent activity.
func TestParseLine_UserNonToolResultIgnored(t *testing.T) {
	line := mustMarshal(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "user injected this"},
			},
		},
	})

	var s runState
	parseLine(line, &s)
	if len(s.turns) != 0 {
		t.Fatalf("user.text should be ignored, got: %+v", s.turns)
	}
}

// Assistant events carry per-turn usage. Every Turn produced from that
// message should get a pointer to the same usage block so the timeline can
// show cache_read vs fresh input next to each turn.
func TestParseLine_AssistantUsageAttachedToTurns(t *testing.T) {
	line := mustMarshal(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "hello"},
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_xyz",
					"name":  "Bash",
					"input": map[string]any{"command": "ls"},
				},
			},
			"usage": map[string]any{
				"input_tokens":                42,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     1000,
				"output_tokens":               12,
			},
		},
	})

	var s runState
	parseLine(line, &s)
	if len(s.turns) != 2 {
		t.Fatalf("want 2 turns, got %d", len(s.turns))
	}
	for i, tr := range s.turns {
		if tr.Usage == nil {
			t.Fatalf("turn %d has nil Usage", i)
		}
		if tr.Usage.CacheReadInputTokens != 1000 {
			t.Fatalf("turn %d wrong usage: %+v", i, tr.Usage)
		}
	}
	// Both turns must point at the same Usage so a downstream consumer
	// doesn't see one-per-message rendered as duplicate budget.
	if s.turns[0].Usage != s.turns[1].Usage {
		t.Fatalf("turns from the same assistant message should share *Usage")
	}
	// tool_use.id must be plumbed through so tool_result can be paired.
	if s.turns[1].ToolUseID != "toolu_xyz" {
		t.Fatalf("tool turn missing id: %+v", s.turns[1])
	}
}

// ContextTokens / CacheReadTokens come from the *last* assistant turn,
// not from result.usage (which double-counts cache reads). Run-level
// totals (InputTokens, etc) come from the result event.
func TestRunState_TotalsAndContextTokens(t *testing.T) {
	first := mustMarshal(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "first"},
			},
			"usage": map[string]any{
				"input_tokens":                100,
				"cache_creation_input_tokens": 5000,
				"cache_read_input_tokens":     0,
				"output_tokens":               50,
			},
		},
	})
	second := mustMarshal(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "second"},
			},
			"usage": map[string]any{
				"input_tokens":                20,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     5100,
				"output_tokens":               80,
			},
		},
	})
	result := mustMarshal(t, map[string]any{
		"type":           "result",
		"result":         "done",
		"session_id":     "sess",
		"total_cost_usd": 0.01,
		"num_turns":      2,
		"duration_ms":    1234,
		"usage": map[string]any{
			"input_tokens":                120,
			"cache_creation_input_tokens": 5000,
			"cache_read_input_tokens":     5100,
			"output_tokens":               130,
		},
	})

	var s runState
	parseLine(first, &s)
	parseLine(second, &s)
	parseLine(result, &s)

	r := s.toResult(s.responseText)
	if r.InputTokens != 120 || r.OutputTokens != 130 {
		t.Fatalf("run totals: %+v", r)
	}
	// last turn (second): 20 + 0 + 5100 = 5120
	if r.ContextTokens != 5120 {
		t.Fatalf("ContextTokens = %d, want 5120", r.ContextTokens)
	}
	if r.CacheReadTokens != 5100 {
		t.Fatalf("CacheReadTokens = %d, want 5100", r.CacheReadTokens)
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
