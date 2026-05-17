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

	// "assistant" and "user" events both carry a message
	Message *Message `json:"message,omitempty"`

	// "result" events carry the run-level usage totals; "assistant" events
	// carry per-turn usage inside .Message.Usage. They are *not* the same
	// number — result totals sum across every turn in the run and so
	// double-count cache reads.
	Usage *Usage `json:"usage,omitempty"`
}

// Message is the shape used by both "assistant" and "user" stream-json
// events. Assistant messages carry text/thinking/tool_use blocks; user
// messages carry tool_result blocks (and sometimes plain text).
type Message struct {
	Content []ContentBlock `json:"content"`
	Usage   *Usage         `json:"usage,omitempty"` // assistant only
}

// AssistantMessage is kept as an alias for backward source compatibility
// with anything outside this package that referenced the old name.
type AssistantMessage = Message

type ContentBlock struct {
	Type string `json:"type"`

	// type=="text" | "thinking"
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`

	// type=="tool_use"
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=="tool_result". Content is either a string or an array of
	// {type:"text"|"image", ...} blocks — kept raw so parseLine can handle
	// either shape.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Usage mirrors the per-turn (assistant message) and per-run (result event)
// usage shape emitted by the Claude CLI. Use the per-turn one to derive
// "current context size" (input + cache_creation + cache_read on the last
// assistant turn); the per-run one is for billing summaries and is not safe
// for sizing because cache reads are counted on every turn.
type Usage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
}

// Turn is one entry in the chronological log of what Claude produced during a
// run: an assistant text message ("text"), an extended-thinking trace
// ("thinking"), a tool invocation ("tool"), or the result of a tool call
// ("tool_result"). Long string fields are clipped on construction so the
// entire structure is always valid JSON when serialized — never a half-cut
// string.
type Turn struct {
	Type string `json:"type"` // "text" | "thinking" | "tool" | "tool_result"

	// type=="text" | "thinking"
	Text string `json:"text,omitempty"`

	// type=="tool"
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"` // parsed object with long strings clipped

	// type=="tool" | "tool_result" — the tool_use id that pairs the two.
	ToolUseID string `json:"tool_use_id,omitempty"`

	// type=="tool_result" — the tool's output. Either a clipped string or
	// (when the tool returned multimodal content) a list of {type, ...}
	// blocks with string leaves clipped.
	Output  any  `json:"output,omitempty"`
	IsError bool `json:"is_error,omitempty"`

	// Per-turn token usage. Populated for assistant turns (text / thinking /
	// tool) when the parent message carried a usage block, which is every
	// assistant event in practice. Lets the timeline show cache_read vs
	// fresh input per turn so callers can eyeball whether caching is
	// working without summing across the whole run.
	Usage *Usage `json:"usage,omitempty"`
}
