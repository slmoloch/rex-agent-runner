// Package workspace centralizes the on-disk layout under the rex workspace dir.
package workspace

import (
	"os"
	"path/filepath"
)

type Paths struct {
	Root          string // absolute workspace path
	Rex           string // <root>/.rex
	TurnMarkerDir string // <root>/.rex/turn-markers
	Inbox         string // <root>/inbox
	EventsDir     string // <root>/events  (hourly JSONL)
	LegacyEvents  string // <root>/events.jsonl (pre-rotation events)
	SessionsFile  string // <root>/sessions.json
	CallbacksFile string // <root>/callbacks.json
	SkillsDir     string // <root>/skills
	AgentMD       string // <root>/AGENT.md
	MemoryMD      string // <root>/MEMORY.md
}

// New resolves workspace-relative paths from an absolute workspace root.
func New(root string) Paths {
	return Paths{
		Root:          root,
		Rex:           filepath.Join(root, ".rex"),
		TurnMarkerDir: filepath.Join(root, ".rex", "turn-markers"),
		Inbox:         filepath.Join(root, "inbox"),
		EventsDir:     filepath.Join(root, "events"),
		LegacyEvents:  filepath.Join(root, "events.jsonl"),
		SessionsFile:  filepath.Join(root, "sessions.json"),
		CallbacksFile: filepath.Join(root, "callbacks.json"),
		SkillsDir:     filepath.Join(root, "skills"),
		AgentMD:       filepath.Join(root, "AGENT.md"),
		MemoryMD:      filepath.Join(root, "MEMORY.md"),
	}
}

// EnsureDirs creates the directories that rex assumes exist at runtime.
func (p Paths) EnsureDirs() error {
	for _, d := range []string{p.Root, p.Rex, p.TurnMarkerDir, p.EventsDir, p.Inbox} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
