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
		tools, _ := encodeJSON(raw["tools"])
		sends, _ := encodeJSON(raw["rex_user_sends"])
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
			tools,
			sends,
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
