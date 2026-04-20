package claude

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// RexUserSend is one record appended by `rex user` during a turn.
// We keep it as a map so we don't have to track every field the CLI writes.
type RexUserSend map[string]any

// drainTurnMarkers reads and deletes <turnMarkerDir>/<turnID>.jsonl, returning
// the parsed lines. A missing file is normal (no `rex user` calls happened).
func drainTurnMarkers(turnMarkerDir, turnID string) []RexUserSend {
	path := filepath.Join(turnMarkerDir, turnID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("open turn marker", "path", path, "err", err)
		}
		return nil
	}
	defer func() {
		f.Close()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("remove turn marker", "path", path, "err", err)
		}
	}()

	var sends []RexUserSend
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec RexUserSend
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		sends = append(sends, rec)
	}
	return sends
}
