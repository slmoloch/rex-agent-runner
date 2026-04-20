// Command rex is the native daemon/CLI for the rex agent runner.
//
// This file currently wires only the modules ported so far (config, claude,
// voice) so the tree builds. Subcommands like `serve`, `config setup`, etc.
// will land as their modules are ported.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/slmoloch/rex-agent-runner/internal/claude"
	"github.com/slmoloch/rex-agent-runner/internal/config"
	"github.com/slmoloch/rex-agent-runner/internal/voice"
)

func main() {
	installDir, err := os.Executable()
	if err != nil {
		installDir = "."
	}
	installDir = filepath.Dir(installDir)

	cfg, err := config.Load(config.DefaultProjectDir(installDir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	// Touch the ported packages so the build catches wiring errors.
	_ = &claude.Runner{
		Bin:     claude.FindBin(cfg.ClaudeBin),
		Workdir: cfg.ProjectDir(),
	}
	if _, err := voice.New(cfg); err != nil {
		// Not fatal — voice is optional. Just report the reason.
		fmt.Fprintln(os.Stderr, "voice:", err)
	}

	fmt.Println("rex (go port) — modules wired; subcommands coming soon")
}
