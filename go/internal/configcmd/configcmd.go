// Package configcmd implements `rex config` and the daemon-control verbs
// (start/stop/restart/status/logs). Setup is cross-platform; the
// start/stop/status verbs delegate to a platform-specific process manager
// (launchd on macOS, systemd --user on Linux).
package configcmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/slmoloch/rex-agent-runner/internal/config"
)

// Setup runs an interactive prompt that writes <projectDir>/config.json.
func Setup(in *bufio.Reader, out io.Writer, projectDir string) error {
	cfg, err := config.Load(projectDir)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "Rex Configuration Setup")
	fmt.Fprintln(out, strings.Repeat("=", 40))

	cfg.TelegramBotToken = promptString(in, out,
		"Telegram bot token", maskSecret(cfg.TelegramBotToken), cfg.TelegramBotToken)

	idsStr := ""
	if len(cfg.AllowedUserIDs) > 0 {
		parts := make([]string, len(cfg.AllowedUserIDs))
		for i, id := range cfg.AllowedUserIDs {
			parts[i] = strconv.FormatInt(id, 10)
		}
		idsStr = strings.Join(parts, ", ")
	}
	idsInput := promptString(in, out,
		"Allowed Telegram user IDs (comma-separated)", idsStr, idsStr)
	if idsInput != "" {
		cfg.AllowedUserIDs = parseIDList(idsInput)
	}

	chatDefault := ""
	if cfg.TelegramChatID != 0 {
		chatDefault = strconv.FormatInt(cfg.TelegramChatID, 10)
	} else if len(cfg.AllowedUserIDs) > 0 {
		chatDefault = strconv.FormatInt(cfg.AllowedUserIDs[0], 10)
	}
	chatStr := promptString(in, out,
		"Telegram chat ID for notifications", chatDefault, chatDefault)
	if chatStr != "" {
		if n, err := strconv.ParseInt(chatStr, 10, 64); err == nil {
			cfg.TelegramChatID = n
		}
	}

	cfg.OpenAIAPIKey = promptString(in, out,
		"OpenAI API key (for voice messages)", maskSecret(cfg.OpenAIAPIKey), cfg.OpenAIAPIKey)

	voiceDefault := cfg.TTSVoice
	if voiceDefault == "" {
		voiceDefault = "nova"
	}
	cfg.TTSVoice = promptString(in, out,
		"TTS voice [alloy|echo|fable|onyx|nova|shimmer|coral|sage|ash]",
		voiceDefault, voiceDefault)

	portDefault := strconv.Itoa(cfg.JobPort)
	if cfg.JobPort == 0 {
		portDefault = "9821"
	}
	portStr := promptString(in, out, "Job HTTP port", portDefault, portDefault)
	if n, err := strconv.Atoi(portStr); err == nil {
		cfg.JobPort = n
	}

	wsDefault := cfg.Workspace
	if wsDefault == "" {
		wsDefault = "./workspace"
	}
	cfg.Workspace = promptString(in, out, "Workspace directory", wsDefault, wsDefault)

	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nConfig saved to %s\n", filepath.Join(projectDir, "config.json"))
	return nil
}

// Show prints the current configuration with secrets masked.
func Show(out io.Writer, cfg *config.Config) {
	fmt.Fprintf(out, "  telegram_bot_token:  %s\n", maskBoth(cfg.TelegramBotToken))
	fmt.Fprintf(out, "  allowed_user_ids:    %v\n", cfg.AllowedUserIDs)
	chat := "(default)"
	if cfg.TelegramChatID != 0 {
		chat = strconv.FormatInt(cfg.TelegramChatID, 10)
	}
	fmt.Fprintf(out, "  telegram_chat_id:    %s\n", chat)
	fmt.Fprintf(out, "  openai_api_key:      %s\n", maskBoth(cfg.OpenAIAPIKey))
	voice := cfg.TTSVoice
	if voice == "" {
		voice = "nova"
	}
	fmt.Fprintf(out, "  tts_voice:           %s\n", voice)
	port := cfg.JobPort
	if port == 0 {
		port = 9821
	}
	fmt.Fprintf(out, "  job_port:            %d\n", port)
	ws := cfg.Workspace
	if ws == "" {
		ws = "./workspace"
	}
	fmt.Fprintf(out, "  workspace:           %s\n", ws)
	fmt.Fprintf(out, "  config file:         %s\n", filepath.Join(cfg.ProjectDir(), "config.json"))
}

// Logs tails log files under <projectDir>/logs. When follow is true, uses
// `tail -f`; otherwise prints the last 50 lines of each file.
func Logs(cfg *config.Config, follow bool) error {
	logDir := filepath.Join(cfg.ProjectDir(), "logs")
	entries, err := os.ReadDir(logDir)
	if err != nil || len(entries) == 0 {
		fmt.Println("No logs yet.")
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".err.log") {
			files = append(files, filepath.Join(logDir, name))
		}
	}
	if len(files) == 0 {
		fmt.Println("No logs yet.")
		return nil
	}
	// Shell out to `tail` — it's available on every target OS and handles -f
	// (follow) natively. Keeps our binary simple.
	args := []string{"-n", "50"}
	if follow {
		args = []string{"-f"}
	}
	return runPassthrough("tail", append(args, files...)...)
}

// --- helpers ---

func promptString(in *bufio.Reader, out io.Writer, label, display, fallback string) string {
	shown := display
	if shown == "" {
		shown = "(not set)"
	}
	fmt.Fprintf(out, "%s [%s]: ", label, shown)
	line, err := in.ReadString('\n')
	if err != nil && err != io.EOF {
		return fallback
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback
	}
	return line
}

func parseIDList(s string) []int64 {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) < 8 {
		return "..."
	}
	return s[:8] + "..."
}

func maskBoth(s string) string {
	if s == "" {
		return "(not set)"
	}
	if len(s) <= 12 {
		return "(not set)"
	}
	return s[:8] + "..." + s[len(s)-4:]
}

// LoadConfig resolves the caller's project dir from ~/.config/rex/project_dir
// and loads config.json under it. Returns an error when init hasn't run.
func LoadConfig() (*config.Config, error) {
	pd, err := ProjectDir()
	if err != nil {
		return nil, err
	}
	return config.Load(pd)
}

// ProjectDir reads ~/.config/rex/project_dir.
func ProjectDir() (string, error) {
	if p := os.Getenv("REX_PROJECT_DIR"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".config", "rex", "project_dir")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("rex: not initialized. Run 'rex init' in your project directory")
	}
	pd := strings.TrimSpace(string(data))
	if pd == "" {
		return "", fmt.Errorf("rex: project_dir file is empty; re-run 'rex init'")
	}
	if _, err := os.Stat(pd); err != nil {
		return "", fmt.Errorf("rex: project dir '%s' not found. Run 'rex init' again", pd)
	}
	return pd, nil
}

// Marshal helper so start/stop/status platforms can share the config path.
func configPath(projectDir string) string {
	return filepath.Join(projectDir, "config.json")
}

// ConfigExists reports whether a config.json is in place.
func ConfigExists(projectDir string) bool {
	_, err := os.Stat(configPath(projectDir))
	return err == nil
}

// Ensure encoding/json stays imported for potential future use.
var _ = json.Marshal
