//go:build linux

package configcmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const unitName = "rex-agent-runner.service"

type systemdController struct{}

// Controller returns the systemd-user-backed controller on Linux.
func Controller() (DaemonController, error) { return systemdController{}, nil }

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", unitName), nil
}

func (systemdController) Start(projectDir, rexBin string) error {
	if !ConfigExists(projectDir) {
		return fmt.Errorf("no config found. Run: rex config setup")
	}
	logDir := filepath.Join(projectDir, "logs")
	_ = os.MkdirAll(logDir, 0o755)

	unit, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return err
	}

	body := fmt.Sprintf(systemdTemplate, rexBin, projectDir, projectDir)
	if err := writeText(unit, body); err != nil {
		return err
	}

	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", unitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable --now: %s: %w", strings.TrimSpace(string(out)), err)
	}
	fmt.Printf("Rex daemon started.\n  Unit: %s\n  Logs: journalctl --user -u %s -f\n", unit, unitName)
	return nil
}

func (systemdController) Stop(projectDir string) error {
	unit, err := unitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(unit); err != nil {
		fmt.Println("Daemon is not installed.")
		return nil
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
	_ = os.Remove(unit)
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("Rex daemon stopped.")
	return nil
}

func (systemdController) Status(projectDir string) (string, error) {
	out, err := exec.Command("systemctl", "--user", "is-active", unitName).CombinedOutput()
	state := strings.TrimSpace(string(out))
	if err != nil || state != "active" {
		if state == "" {
			state = "not running"
		}
		return state, nil
	}
	return "running", nil
}

const systemdTemplate = `[Unit]
Description=Rex agent runner
After=network.target

[Service]
Type=simple
ExecStart=%s serve
WorkingDirectory=%s
Restart=always
RestartSec=5
Environment=REX_PROJECT_DIR=%s

[Install]
WantedBy=default.target
`
