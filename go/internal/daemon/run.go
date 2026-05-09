// Package daemon is the long-running rex process: Telegram bot, HTTP
// dashboard, scheduled callbacks, session gc, and the daily-reset loop.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/assets"
	"github.com/slmoloch/rex-agent-runner/internal/callback"
	"github.com/slmoloch/rex-agent-runner/internal/claude"
	"github.com/slmoloch/rex-agent-runner/internal/config"
	"github.com/slmoloch/rex-agent-runner/internal/events"
	"github.com/slmoloch/rex-agent-runner/internal/server"
	"github.com/slmoloch/rex-agent-runner/internal/session"
	"github.com/slmoloch/rex-agent-runner/internal/skills"
	"github.com/slmoloch/rex-agent-runner/internal/telegram"
	"github.com/slmoloch/rex-agent-runner/internal/timeline"
	"github.com/slmoloch/rex-agent-runner/internal/voice"
	"github.com/slmoloch/rex-agent-runner/internal/workspace"
)

type Daemon struct {
	cfg       *config.Config
	ws        workspace.Paths
	runner    *claude.Runner
	events    *events.Store
	timeline  *timeline.Store
	sessions  *session.Store
	callbacks *callback.Store
	tg        *telegram.Client
	voice     *voice.Client // nil when not configured
	allowed   map[int64]struct{}

	systemPrompt string
	jobPort      int
}

// Run starts every subsystem and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	if cfg.TelegramBotToken == "" {
		return fmt.Errorf("telegram_bot_token not set — run `rex config setup`")
	}
	installDir := installDir()

	paths, err := cfg.ResolvePaths(installDir)
	if err != nil {
		return err
	}
	ws := workspace.New(paths.Workspace)
	if err := ws.EnsureDirs(); err != nil {
		return err
	}

	tlStore, err := timeline.Open(filepath.Join(ws.Root, "events.db"))
	if err != nil {
		return fmt.Errorf("open timeline db: %w", err)
	}
	evStore := events.NewStore(ws.EventsDir, ws.LegacyEvents, tlStore)
	seStore := session.NewStore(ws.SessionsFile, evStore)
	cbStore := callback.NewStore(ws.CallbacksFile)

	chat := cfg.TelegramChatID
	if chat == 0 && len(cfg.AllowedUserIDs) > 0 {
		chat = cfg.AllowedUserIDs[0]
	}
	tg := telegram.New(cfg.TelegramBotToken, chat)

	vc, vcErr := voice.New(cfg)
	if vcErr != nil {
		slog.Info("voice disabled", "reason", vcErr)
		vc = nil
	}

	allowed := make(map[int64]struct{}, len(cfg.AllowedUserIDs))
	for _, id := range cfg.AllowedUserIDs {
		allowed[id] = struct{}{}
	}

	port := cfg.JobPort
	if port == 0 {
		port = 9821
	}

	systemPrompt, err := buildSystemPrompt(ws)
	if err != nil {
		return err
	}

	d := &Daemon{
		cfg:          cfg,
		ws:           ws,
		runner:       &claude.Runner{Bin: claude.FindBin(cfg.ClaudeBin), Workdir: ws.Root, TurnMarkerDir: ws.TurnMarkerDir},
		events:       evStore,
		timeline:     tlStore,
		sessions:     seStore,
		callbacks:    cbStore,
		tg:           tg,
		voice:        vc,
		allowed:      allowed,
		systemPrompt: systemPrompt,
		jobPort:      port,
	}

	d.cleanupStaleTurnMarkers()

	srv, err := server.Listen(port, server.New(tlStore, seStore, cbStore, d.RunJob).Routes())
	if err != nil {
		return err
	}
	defer shutdown(srv)
	defer tlStore.Close()

	go callback.Run(ctx, cbStore, ws.Root, d.callbackSubmitter())
	go d.gcLoop(ctx)
	go d.dailyResetLoop(ctx)
	go d.retentionLoop(ctx)

	slog.Info("rex daemon started", "dashboard", fmt.Sprintf("http://127.0.0.1:%d/", port))
	return d.pollTelegram(ctx)
}

// submitterFunc adapts a plain function to callback.Submitter.
type submitterFunc func(ctx context.Context, prompt, jobName, sessionTarget string)

func (f submitterFunc) Submit(ctx context.Context, prompt, jobName, sessionTarget string) {
	f(ctx, prompt, jobName, sessionTarget)
}

// callbackSubmitter returns the Submitter the scheduler uses to fire callbacks.
func (d *Daemon) callbackSubmitter() callback.Submitter {
	return submitterFunc(func(ctx context.Context, prompt, jobName, sessionTarget string) {
		_, _ = d.runInSession(ctx, prompt, sessionTarget, jobName, "")
	})
}

// buildSystemPrompt concatenates workspace skills + the embedded rex prompt +
// workspace AGENT.md, matching what claude_runner.py did at import time.
func buildSystemPrompt(ws workspace.Paths) (string, error) {
	var parts []string
	if combined, err := skills.LoadCombined(ws.SkillsDir); err == nil && combined != "" {
		parts = append(parts, combined)
	}
	parts = append(parts, string(assets.RexSystemPrompt))
	if data, err := os.ReadFile(ws.AgentMD); err == nil {
		parts = append(parts, string(data))
	}
	return joinNonEmpty(parts, "\n\n"), nil
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

const staleMarkerAge = 24 * time.Hour

func (d *Daemon) cleanupStaleTurnMarkers() {
	entries, err := os.ReadDir(d.ws.TurnMarkerDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleMarkerAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(d.ws.TurnMarkerDir + string(os.PathSeparator) + e.Name())
		}
	}
}

func shutdown(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func installDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	// InstallDir is the dir containing the binary.
	for i := len(exe) - 1; i >= 0; i-- {
		if exe[i] == '/' {
			return exe[:i]
		}
	}
	return "."
}

