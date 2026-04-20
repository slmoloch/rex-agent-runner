// Package initflow implements `rex init` — scaffolds a project directory and
// records it as the active rex project.
package initflow

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"github.com/slmoloch/rex-agent-runner/internal/assets"
	"github.com/slmoloch/rex-agent-runner/internal/configcmd"
	"github.com/slmoloch/rex-agent-runner/internal/workspace"
)

// ProjectDirFile is where the canonical project directory path is stored.
// Mirrors ~/.config/rex/project_dir from the Python implementation.
func ProjectDirFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "rex", "project_dir"), nil
}

// Run initialises the current directory as a rex project and runs config setup.
func Run(in *bufio.Reader, out *os.File) error {
	projectDir, err := os.Getwd()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Initializing rex project in: %s\n\n", projectDir)

	ws := workspace.New(filepath.Join(projectDir, "workspace"))
	if err := ws.EnsureDirs(); err != nil {
		return err
	}
	for _, d := range []string{ws.SkillsDir, filepath.Join(projectDir, "logs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	scaffold := []struct {
		path, content string
	}{
		{ws.AgentMD, assets.AgentMD},
		{ws.MemoryMD, assets.MemoryMD},
	}
	for _, s := range scaffold {
		if _, err := os.Stat(s.path); err == nil {
			rel, _ := filepath.Rel(projectDir, s.path)
			fmt.Fprintf(out, "  Skipped %s (already exists)\n", rel)
			continue
		}
		if err := os.WriteFile(s.path, []byte(s.content), 0o644); err != nil {
			return err
		}
		rel, _ := filepath.Rel(projectDir, s.path)
		fmt.Fprintf(out, "  Created %s\n", rel)
	}

	pdFile, err := ProjectDirFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pdFile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(pdFile, []byte(projectDir+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n  Project dir saved to %s\n\n", pdFile)

	return configcmd.Setup(in, out, projectDir)
}
