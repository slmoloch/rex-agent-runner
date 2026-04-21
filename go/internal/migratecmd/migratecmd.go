// Package migratecmd implements `rex migrate-from-python`, a one-shot
// cleanup for users upgrading from the Python implementation.
//
// TRANSITIONAL: this command exists only to clean up state the Python
// install left on disk (launchd plists + venv). Target removal:
// 2026-10-01 (six months after the Go rewrite shipped). Delete this
// package and its wiring in cmd/rex/main.go when that date passes.
package migratecmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/slmoloch/rex-agent-runner/internal/configcmd"
)

// Run performs the cleanup. Each step is best-effort — missing files /
// already-unloaded plists are not errors. The function reports what it
// actually did so the user can see the side effects.
func Run(out io.Writer) error {
	projectDir, err := configcmd.ProjectDir()
	if err != nil {
		// No project dir means nothing to migrate — the user never ran the
		// Python version either. Treat as success.
		fmt.Fprintln(out, "No rex project configured; nothing to migrate.")
		return nil
	}

	if runtime.GOOS == "darwin" {
		cleanupLaunchAgents(out)
	} else {
		fmt.Fprintf(out, "Skipping launchd cleanup (not macOS).\n")
	}

	cleanupVenv(out, projectDir)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Done. Next: `rex config start` to install the native daemon unit.")
	return nil
}

// cleanupLaunchAgents removes the Python-era daemon plist and every
// per-callback plist from ~/Library/LaunchAgents.
func cleanupLaunchAgents(out io.Writer) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(out, "launchd cleanup skipped: could not resolve $HOME.")
		return
	}
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")

	// The old daemon plist.
	daemonPlist := filepath.Join(agentsDir, "com.rex.agent-runner.plist")
	if _, err := os.Stat(daemonPlist); err == nil {
		_ = exec.Command("launchctl", "unload", daemonPlist).Run()
		if err := os.Remove(daemonPlist); err == nil {
			fmt.Fprintf(out, "Removed %s\n", daemonPlist)
		} else {
			fmt.Fprintf(out, "WARN: could not remove %s: %v\n", daemonPlist, err)
		}
	}

	// Per-callback plists. Python wrote one per callback with the prefix
	// com.rex.callback.<id>.plist — the Go scheduler runs in-daemon so none
	// of them are needed anymore.
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return
	}
	count := 0
	for _, e := range entries {
		name := e.Name()
		const prefix = "com.rex.callback."
		const suffix = ".plist"
		if len(name) <= len(prefix)+len(suffix) ||
			name[:len(prefix)] != prefix ||
			name[len(name)-len(suffix):] != suffix {
			continue
		}
		plist := filepath.Join(agentsDir, name)
		_ = exec.Command("launchctl", "unload", plist).Run()
		if err := os.Remove(plist); err == nil {
			count++
		}
	}
	if count > 0 {
		fmt.Fprintf(out, "Removed %d callback plist(s) from %s\n", count, agentsDir)
	}
}

// cleanupVenv removes <projectDir>/venv if it exists.
func cleanupVenv(out io.Writer, projectDir string) {
	venv := filepath.Join(projectDir, "venv")
	info, err := os.Stat(venv)
	if err != nil {
		return
	}
	if !info.IsDir() {
		return
	}
	if err := os.RemoveAll(venv); err != nil {
		fmt.Fprintf(out, "WARN: could not remove %s: %v\n", venv, err)
		return
	}
	fmt.Fprintf(out, "Removed %s\n", venv)
}
