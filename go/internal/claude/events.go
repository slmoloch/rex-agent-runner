package claude

import "encoding/json"

// Event is one stream-json line from `claude -p --output-format stream-json`.
// We only decode the fields rex cares about; everything else stays in raw.
type Event struct {
	Type string `json:"type"`

	// "result" events
	Result       string  `json:"result,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`
	DurationMS   int     `json:"duration_ms,omitempty"`

	// "assistant" events
	Message *AssistantMessage `json:"message,omitempty"`
}

type AssistantMessage struct {
	Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Turn is one entry in the chronological log of what Claude produced during a
// run: either an assistant text message ("text") or a tool invocation ("tool").
// Long string fields are clipped on construction so the entire structure is
// always valid JSON when serialized — never a half-cut string.
type Turn struct {
	Type string `json:"type"` // "text" | "tool"

	// type=="text"
	Text string `json:"text,omitempty"`

	// type=="tool"
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"` // parsed object with long strings clipped
}
