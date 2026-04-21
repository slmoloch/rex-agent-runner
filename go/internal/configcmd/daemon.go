package configcmd

import (
	"io"
	"os"
	"os/exec"
)

// Start, Stop, Restart, Status delegate to the platform-specific
// implementation declared in the GOOS-tagged files below.

// DaemonController is implemented by the platform-specific process managers.
type DaemonController interface {
	Start(projectDir, rexBin string) error
	Stop(projectDir string) error
	Status(projectDir string) (string, error)
}

// Restart is the same on every platform.
func Restart(c DaemonController, projectDir, rexBin string) error {
	_ = c.Stop(projectDir) // best-effort
	return c.Start(projectDir, rexBin)
}

// runPassthrough executes cmd with args and streams stdin/stdout/stderr.
func runPassthrough(cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// writeText is a tiny helper shared by both platform backends.
func writeText(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// readAll is here to keep `io` imported for platform files that might not
// use it directly. Safe no-op.
var _ = io.ReadAll
