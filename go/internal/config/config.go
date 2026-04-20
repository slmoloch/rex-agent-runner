// Package config loads and persists rex's JSON config file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	TelegramBotToken string  `json:"telegram_bot_token,omitempty"`
	AllowedUserIDs   []int64 `json:"allowed_user_ids,omitempty"`
	TelegramChatID   int64   `json:"telegram_chat_id,omitempty"`

	OpenAIAPIKey string `json:"openai_api_key,omitempty"`
	WhisperModel string `json:"whisper_model,omitempty"`
	TTSModel     string `json:"tts_model,omitempty"`
	TTSVoice     string `json:"tts_voice,omitempty"`

	JobPort   int    `json:"job_port,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	ClaudeBin string `json:"claude_bin,omitempty"`

	projectDir string
}

// Paths groups the resolved filesystem locations derived from a Config.
// Kept separate from Config so the on-disk JSON stays minimal.
type Paths struct {
	ProjectDir     string // where config.json lives
	Workspace      string // absolute
	RexDir         string // <workspace>/.rex
	TurnMarkerDir  string // <workspace>/.rex/turn-markers
	InstallDir     string // dir of the rex binary / assets
	ConfigFile     string
}

// ProjectDir returns the directory holding config.json.
func (c *Config) ProjectDir() string { return c.projectDir }

// DefaultProjectDir honors $REX_PROJECT_DIR, falling back to installDir.
func DefaultProjectDir(installDir string) string {
	if p := os.Getenv("REX_PROJECT_DIR"); p != "" {
		return p
	}
	return installDir
}

// Load reads config.json from projectDir. A missing file yields a zero-valued
// Config; callers that require specific fields should check them explicitly.
func Load(projectDir string) (*Config, error) {
	path := filepath.Join(projectDir, "config.json")
	cfg := &Config{projectDir: projectDir}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Save atomically writes the config back to disk.
func (c *Config) Save() error {
	path := filepath.Join(c.projectDir, "config.json")
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ResolvePaths expands the workspace and derived dirs relative to projectDir.
func (c *Config) ResolvePaths(installDir string) (Paths, error) {
	ws := c.Workspace
	if ws == "" {
		ws = "./workspace"
	}
	if !filepath.IsAbs(ws) {
		ws = filepath.Join(c.projectDir, ws)
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return Paths{}, err
	}
	rex := filepath.Join(abs, ".rex")
	return Paths{
		ProjectDir:    c.projectDir,
		Workspace:     abs,
		RexDir:        rex,
		TurnMarkerDir: filepath.Join(rex, "turn-markers"),
		InstallDir:    installDir,
		ConfigFile:    filepath.Join(c.projectDir, "config.json"),
	}, nil
}
