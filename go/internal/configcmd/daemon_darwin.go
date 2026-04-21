//go:build darwin

package configcmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	plistLabel = "com.rex.agent-runner"
)

type launchdController struct{}

// Controller returns the launchd-backed controller on macOS.
func Controller() (DaemonController, error) { return launchdController{}, nil }

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist"), nil
}

func (launchdController) Start(projectDir, rexBin string) error {
	if !ConfigExists(projectDir) {
		return fmt.Errorf("no config found. Run: rex config setup")
	}
	logDir := filepath.Join(projectDir, "logs")
	_ = os.MkdirAll(logDir, 0o755)

	plistFile, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistFile), 0o755); err != nil {
		return err
	}

	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	body := fmt.Sprintf(plistTemplate, plistLabel, rexBin, projectDir,
		filepath.Join(logDir, "daemon.log"),
		filepath.Join(logDir, "daemon.err.log"),
		path, projectDir)
	if err := writeText(plistFile, body); err != nil {
		return err
	}

	// Unload first so repeated starts are idempotent.
	_ = exec.Command("launchctl", "unload", plistFile).Run()
	out, err := exec.Command("launchctl", "load", plistFile).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl load: %s: %w", strings.TrimSpace(string(out)), err)
	}
	fmt.Printf("Rex daemon started.\n  Logs: %s/daemon.log\n", logDir)
	return nil
}

func (launchdController) Stop(projectDir string) error {
	plistFile, err := plistPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(plistFile); err != nil {
		fmt.Println("Daemon is not installed.")
		return nil
	}
	out, err := exec.Command("launchctl", "unload", plistFile).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "Could not find") {
		return fmt.Errorf("launchctl unload: %s", strings.TrimSpace(string(out)))
	}
	_ = os.Remove(plistFile)
	fmt.Println("Rex daemon stopped.")
	return nil
}

func (launchdController) Status(projectDir string) (string, error) {
	out, err := exec.Command("launchctl", "list", plistLabel).CombinedOutput()
	if err != nil {
		return "not running", nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, `"PID"`) {
			pid := strings.TrimSpace(strings.TrimSuffix(strings.SplitN(line, "=", 2)[1], ";"))
			return "running (PID " + pid + ")", nil
		}
	}
	return "loaded (not currently running)", nil
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
    <array>
      <string>%s</string>
      <string>serve</string>
    </array>
  <key>WorkingDirectory</key><string>%s</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>EnvironmentVariables</key>
    <dict>
      <key>PATH</key><string>%s</string>
      <key>REX_PROJECT_DIR</key><string>%s</string>
    </dict>
</dict>
</plist>
`
