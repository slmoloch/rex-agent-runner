package timeline

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"io"
)

// ingest reads a JSONL stream and inserts each parseable line via stmt.
// Lines we can't parse are skipped (matching rex_timeline.py:rebuild_from_logs).
func ingest(stmt *sql.Stmt, r io.Reader) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	count := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		// Prefer the new `turns` key; older JSONL files only have `tools`,
		// which we adapt to a turns-shaped list so the DB stays homogeneous.
		var turnsVal any
		if v, ok := raw["turns"]; ok {
			turnsVal = v
		} else {
			turnsVal = legacyToolsToTurns(raw["tools"])
		}
		turns, _ := encodeJSON(turnsVal)
		var repliesEncoded any
		if v, ok := raw["replies"]; ok && v != nil {
			repliesEncoded, _ = encodeJSON(v)
		}
		if _, err := stmt.Exec(
			asStr(raw["timestamp"]),
			asStr(raw["session"]),
			asStr(raw["session_id"]),
			asStr(raw["trigger"]),
			asStr(raw["prompt_preview"]),
			asStr(raw["response_preview"]),
			asFloat(raw["cost_usd"]),
			asInt(raw["duration_ms"]),
			asInt(raw["num_turns"]),
			asStr(raw["caller_session"]),
			turns,
			boolAsInt(raw["reply_sent"]),
			repliesEncoded,
		); err != nil {
			return count, err
		}
		count++
	}
	return count, sc.Err()
}

func asStr(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func boolAsInt(v any) int {
	b, _ := v.(bool)
	if b {
		return 1
	}
	return 0
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// legacyToolsToTurns converts the pre-turns `tools` JSONL field — a list of
// {name, input(string)} — into the unified turns shape so old logs replay
// cleanly into the new DB column. Inputs that happen to be valid JSON are
// reparsed into objects; otherwise they're kept as raw strings.
func legacyToolsToTurns(v any) any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		turn := map[string]any{"type": "tool"}
		if name, ok := m["name"].(string); ok {
			turn["name"] = name
		}
		if input, ok := m["input"]; ok {
			if s, ok := input.(string); ok {
				var parsed any
				if err := json.Unmarshal([]byte(s), &parsed); err == nil {
					turn["input"] = parsed
				} else {
					turn["input"] = s
				}
			} else {
				turn["input"] = input
			}
		}
		out = append(out, turn)
	}
	return out
}
