// Command rex is the native daemon/CLI for the rex agent runner.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/callback"
	"github.com/slmoloch/rex-agent-runner/internal/claude"
	"github.com/slmoloch/rex-agent-runner/internal/config"
	"github.com/slmoloch/rex-agent-runner/internal/configcmd"
	"github.com/slmoloch/rex-agent-runner/internal/daemon"
	"github.com/slmoloch/rex-agent-runner/internal/dispatchcmd"
	"github.com/slmoloch/rex-agent-runner/internal/events"
	"github.com/slmoloch/rex-agent-runner/internal/initflow"
	"github.com/slmoloch/rex-agent-runner/internal/session"
	"github.com/slmoloch/rex-agent-runner/internal/skills"
	"github.com/slmoloch/rex-agent-runner/internal/timeline"
	"github.com/slmoloch/rex-agent-runner/internal/usercmd"
	"github.com/slmoloch/rex-agent-runner/internal/workspace"
)

const usage = `rex - Claude Code Agent Runner

Usage: rex <command> [args]

Setup:
  init                             Initialize project in current directory

Daemon:
  serve                            Run the daemon in the foreground
  start                            Install and start the daemon (launchd/systemd)
  stop                             Stop the daemon
  restart                          Stop then start
  status                           Check daemon status
  logs [-f]                        Show daemon logs

Config:
  config                           Show config and daemon status
  config setup                     Interactive configuration

Skills:
  skills [list]                    List workspace skills

Timeline:
  timeline stats                   Show event-log statistics

Sessions:
  session reset                    Reset the main session
  session list                     List tracked sessions
  session gc                       Run gc pass
  dispatch <session> <message>     Dispatch a prompt via the bot

Callbacks:
  callback list
  callback create "<prompt>" --schedule "<cron>"|--at "<when>" [--session <s>] [--name <id>] [--command "<cmd>"]
  callback remove <id>

User channel (called by agents):
  user text <message>
  user voice <message>
  user file <path> [caption]`

func main() {
	if err := run(); err != nil {
		var eu *usercmd.ExitUnavailable
		if errors.As(err, &eu) {
			fmt.Fprintln(os.Stderr, eu.Reason)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	initLogging()

	if len(os.Args) < 2 {
		fmt.Println(usage)
		os.Exit(1)
	}
	cmd, args := os.Args[1], os.Args[2:]

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch cmd {
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	case "init":
		return initflow.Run(bufio.NewReader(os.Stdin), os.Stdout)
	case "serve":
		return runServe(ctx)
	case "config":
		return runConfig(args)
	case "start", "stop", "restart", "status":
		return runDaemonControl(cmd)
	case "logs":
		return runLogs(args)
	case "skills":
		return runSkills(args)
	case "timeline":
		return runTimeline(args)
	case "session":
		return runSession(ctx, args)
	case "dispatch":
		cfg, err := configcmd.LoadConfig()
		if err != nil {
			return err
		}
		return dispatchcmd.Run(ctx, cfg, args)
	case "callback":
		return runCallback(args)
	case "user":
		cfg, err := configcmd.LoadConfig()
		if err != nil {
			return err
		}
		ws := workspace.New(mustWorkspaceDir(cfg))
		return usercmd.Run(ctx, cfg, ws, args)
	case "version":
		fmt.Println("rex (go port) — dev")
		return nil
	}
	fmt.Fprintf(os.Stderr, "rex: unknown command '%s'\n\n", cmd)
	fmt.Println(usage)
	os.Exit(1)
	return nil
}

func initLogging() {
	// Compact human-friendly text logs to stderr; process managers capture it.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
}

// runServe blocks running the daemon in the foreground.
func runServe(ctx context.Context) error {
	cfg, err := configcmd.LoadConfig()
	if err != nil {
		return err
	}
	return daemon.Run(ctx, cfg)
}

func runConfig(args []string) error {
	if len(args) == 0 {
		cfg, err := configcmd.LoadConfig()
		if err != nil {
			return err
		}
		configcmd.Show(os.Stdout, cfg)
		fmt.Println()
		return runDaemonControl("status")
	}
	switch args[0] {
	case "setup":
		pd, err := configcmd.ProjectDir()
		if err != nil {
			return err
		}
		return configcmd.Setup(bufio.NewReader(os.Stdin), os.Stdout, pd)
	case "show":
		cfg, err := configcmd.LoadConfig()
		if err != nil {
			return err
		}
		configcmd.Show(os.Stdout, cfg)
		return nil
	case "start", "stop", "restart", "status":
		return runDaemonControl(args[0])
	case "logs":
		return runLogs(args[1:])
	}
	return fmt.Errorf("unknown config command: %s", args[0])
}

func runDaemonControl(verb string) error {
	ctrl, err := configcmd.Controller()
	if err != nil {
		return err
	}
	pd, err := configcmd.ProjectDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	switch verb {
	case "start":
		return ctrl.Start(pd, exe)
	case "stop":
		return ctrl.Stop(pd)
	case "restart":
		return configcmd.Restart(ctrl, pd, exe)
	case "status":
		s, err := ctrl.Status(pd)
		if err != nil {
			return err
		}
		cfg, _ := configcmd.LoadConfig()
		port := 9821
		if cfg != nil && cfg.JobPort != 0 {
			port = cfg.JobPort
		}
		fmt.Printf("Rex daemon: %s\n", s)
		fmt.Printf("Dashboard:  http://127.0.0.1:%d/\n", port)
		return nil
	}
	return fmt.Errorf("unknown verb: %s", verb)
}

func runLogs(args []string) error {
	follow := false
	for _, a := range args {
		if a == "-f" {
			follow = true
		}
	}
	cfg, err := configcmd.LoadConfig()
	if err != nil {
		return err
	}
	return configcmd.Logs(cfg, follow)
}

func runSkills(args []string) error {
	cfg, err := configcmd.LoadConfig()
	if err != nil {
		return err
	}
	ws := workspace.New(mustWorkspaceDir(cfg))
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list", "":
		list, err := skills.List(ws.SkillsDir)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Printf("No skills found in %s/\n", ws.SkillsDir)
			return nil
		}
		fmt.Println("Skills:")
		for _, s := range list {
			fmt.Printf("  %-20s %s\n", s.Name, s.Description)
		}
		return nil
	case "help", "-h", "--help":
		fmt.Println("Usage: rex skills [list]")
		return nil
	}
	return fmt.Errorf("unknown skills command: %s", sub)
}

func runTimeline(args []string) error {
	if len(args) == 0 {
		fmt.Println("Usage: rex timeline <stats|rebuild|clear>")
		return nil
	}
	cfg, err := configcmd.LoadConfig()
	if err != nil {
		return err
	}
	ws := workspace.New(mustWorkspaceDir(cfg))
	dbPath := filepath.Join(ws.Root, "events.db")

	// Rebuild has to work on a stale DB — that's the reason the user is
	// running it. So it bypasses the schema check; everything else opens
	// normally and surfaces ErrStaleSchema with a "run rebuild" hint.
	if args[0] == "rebuild" {
		paths := events.JSONLFiles(ws.EventsDir, ws.LegacyEvents)
		if len(paths) == 0 {
			fmt.Println("No JSONL files to rebuild from.")
			return nil
		}
		n, err := timeline.RebuildAt(dbPath, paths)
		if err != nil {
			return err
		}
		fmt.Printf("Rebuilt %d events into %s\n", n, dbPath)
		return nil
	}

	tl, err := timeline.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open timeline db: %w", err)
	}
	defer tl.Close()

	switch args[0] {
	case "stats":
		st, err := tl.Stats()
		if err != nil {
			return err
		}
		fmt.Printf("Events:   %d\n", st.Total)
		fmt.Printf("Sessions: %d\n", st.Sessions)
		fmt.Printf("Range:    %s to %s\n", orDash(st.Oldest), orDash(st.Newest))
		fmt.Printf("Cost:     $%.4f\n", st.Cost)
		return nil
	case "clear":
		if err := tl.Clear(); err != nil {
			return err
		}
		fmt.Printf("Cleared events table. JSONL audit log under %s is untouched.\n", ws.EventsDir)
		return nil
	}
	return fmt.Errorf("unknown timeline command: %s", args[0])
}

func runSession(ctx context.Context, args []string) error {
	cfg, err := configcmd.LoadConfig()
	if err != nil {
		return err
	}
	ws := workspace.New(mustWorkspaceDir(cfg))

	if len(args) == 0 {
		fmt.Println("Usage: rex session <reset|list|gc>")
		return nil
	}
	switch args[0] {
	case "reset":
		tl, err := timeline.Open(filepath.Join(ws.Root, "events.db"))
		if err != nil {
			return fmt.Errorf("open timeline db: %w", err)
		}
		defer tl.Close()
		evStore := events.NewStore(ws.EventsDir, ws.LegacyEvents, tl)
		seStore := session.NewStore(ws.SessionsFile, evStore)

		mainID := seStore.GetMainID()
		if mainID != "" {
			fmt.Println("Preparing session for reset…")
			runner := &claude.Runner{Bin: claude.FindBin(cfg.ClaudeBin), Workdir: ws.Root, TurnMarkerDir: ws.TurnMarkerDir}
			cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			if _, err := runner.Run(cctx, claude.Options{
				Prompt:    "Heads up: your session is about to be reset.",
				SessionID: mainID,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: preparation prompt failed: %v\n", err)
			}
		}
		seStore.ResetMain()
		fmt.Println("Main session reset. Will start fresh on next message.")
		return nil
	case "list":
		seStore := session.NewStore(ws.SessionsFile, events.NewStore(ws.EventsDir, ws.LegacyEvents, nil))
		tracked := seStore.Tracked()
		mainID := seStore.GetMainID()
		if len(tracked) == 0 {
			fmt.Println("No tracked sessions.")
			return nil
		}
		ids := make([]string, 0, len(tracked))
		for sid := range tracked {
			ids = append(ids, sid)
		}
		sort.Strings(ids)
		fmt.Println("Tracked sessions:")
		for _, sid := range ids {
			info := tracked[sid]
			tag := ""
			if info.Name != "" {
				tag = " [" + info.Name + "]"
			}
			main := ""
			if sid == mainID {
				main = " (main)"
			}
			fmt.Printf("  %s%s%s  last_activity=%s\n", sid, tag, main, info.LastActivity)
		}
		return nil
	case "gc":
		fmt.Println("GC is run by the daemon on a 30m schedule; no action taken here.")
		return nil
	}
	return fmt.Errorf("unknown session command: %s", args[0])
}

func runCallback(args []string) error {
	cfg, err := configcmd.LoadConfig()
	if err != nil {
		return err
	}
	ws := workspace.New(mustWorkspaceDir(cfg))
	store := callback.NewStore(ws.CallbacksFile)

	if len(args) == 0 {
		fmt.Println(callbackUsage)
		return nil
	}
	switch args[0] {
	case "list":
		cbs := store.List()
		if len(cbs) == 0 {
			fmt.Println("No callbacks registered.")
			return nil
		}
		fmt.Println("Callbacks:")
		for _, cb := range cbs {
			timing := ""
			if cb.Recurring {
				timing = "recurring: " + cb.Schedule
			} else {
				timing = "once: " + cb.At
			}
			fmt.Printf("  %s: [%s] [session: %s] %s\n", cb.ID, timing, cb.Session, truncate(cb.Prompt, 50))
		}
		return nil
	case "create":
		return createCallback(store, args[1:])
	case "remove":
		if len(args) < 2 {
			return errors.New("Usage: rex callback remove <id>")
		}
		return store.Remove(args[1])
	case "help", "-h", "--help":
		fmt.Println(callbackUsage)
		return nil
	}
	return fmt.Errorf("unknown callback command: %s", args[0])
}

const callbackUsage = `Usage: rex callback <command> [args]

Commands:
  list                                          List all callbacks
  create "<prompt>" --schedule "<cron>" [options]
                                                Create a recurring callback
  create "<prompt>" --at "<when>" [options]     Create a one-time callback
  remove <id>                                   Remove a callback

Options:
  --name <id>           Custom callback ID
  --session <target>    Session to run in: "main" or "new" (default: new)
  --command "<cmd>"     Bash command to run before the prompt. Non-zero exit
                        skips the prompt; zero-exit stdout is appended to it.

--at formats: "HH:MM", "YYYY-MM-DD HH:MM", "+5m", "+2h"`

func createCallback(store *callback.Store, args []string) error {
	if len(args) == 0 {
		return errors.New(`Usage: rex callback create "<prompt>" --schedule "<cron>" | --at "<when>" [options]`)
	}
	prompt := args[0]
	args = args[1:]

	var schedule, at, name, command, sessionTarget string
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			break
		}
		v := args[i+1]
		switch args[i] {
		case "--schedule":
			schedule = v
			i++
		case "--at":
			at = v
			i++
		case "--name":
			name = v
			i++
		case "--command":
			command = v
			i++
		case "--session":
			sessionTarget = v
			i++
		}
	}
	if (schedule == "") == (at == "") {
		return errors.New("provide exactly one of --schedule / --at")
	}
	if sessionTarget == "" {
		sessionTarget = "new"
	}
	cb := &callback.Callback{
		Prompt:    prompt,
		Recurring: schedule != "",
		Schedule:  schedule,
		Session:   sessionTarget,
		Command:   command,
	}
	if at != "" {
		t, err := callback.ParseAt(at, time.Now())
		if err != nil {
			return err
		}
		cb.At = callback.AtFormat(t)
	}
	id, err := store.Add(name, cb)
	if err != nil {
		return err
	}
	if cb.Recurring {
		fmt.Printf("Created recurring callback: %s\n", id)
	} else {
		fmt.Printf("Created one-time callback: %s (at %s)\n", id, cb.At)
	}
	fmt.Printf("  Session: %s\n", sessionTarget)
	if command != "" {
		fmt.Printf("  Pre-check command: %s\n", command)
	}
	return nil
}

func mustWorkspaceDir(cfg *config.Config) string {
	ws := cfg.Workspace
	if ws == "" {
		ws = "./workspace"
	}
	if !filepath.IsAbs(ws) {
		ws = filepath.Join(cfg.ProjectDir(), ws)
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return ws
	}
	return abs
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// strings imported to silence unused if no callers need it locally.
var _ = strings.Join
