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

// ToolCall is a summary of one tool_use content block observed on the stream.
type ToolCall struct {
	Name  string `json:"name"`
	Input string `json:"input"` // JSON-encoded, clipped for logs
}
