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
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/applog"
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
  user text <message>             Plain inline text message (no markup)
  user rich-text <message>        Inline message with Telegram HTML formatting
  user voice <message>            Text-to-speech voice note
  user file <path> [caption]      Upload <path> as a Telegram attachment (not inline)

Other:
  version                          Show version, commit, and build info

Run 'rex <command> --help' (or '-h') for command-specific usage.`

const initUsage = `Usage: rex init

Scaffold workspace/ with agent.md and memory.md in the current directory,
record this directory as the active rex project, and run interactive
config setup.`

const serveUsage = `Usage: rex serve

Run the daemon in the foreground (no process manager). Logs are written
to <project>/logs and mirrored to stderr.`

const daemonControlUsage = `Usage: rex <start|stop|restart|status>

Install/start, stop, restart, or check the rex daemon. Uses launchd on
macOS and systemd --user on Linux.`

const logsUsage = `Usage: rex logs [-f]

Print the last 50 lines of each daemon log file under <project>/logs.
Use -f to follow new output like 'tail -f'.`

const configUsageMsg = `Usage: rex config [subcommand]

No subcommand: show config and daemon status.

Subcommands:
  setup                  Interactive configuration
  show                   Show config (no daemon status)
  start|stop|restart|status   Daemon control (aliases for 'rex <verb>')
  logs [-f]              Show daemon logs`

const skillsUsageMsg = `Usage: rex skills [list]

  list (default)         List workspace skills`

const timelineUsage = `Usage: rex timeline <stats|rebuild|clear>

  stats                  Show event-log statistics
  rebuild                Rebuild the timeline DB from JSONL audit logs
  clear                  Clear the events DB (JSONL audit log untouched)`

const sessionUsage = `Usage: rex session <reset|list|gc>

  reset                  Reset the main session (fresh on next message)
  list                   List tracked sessions
  gc                     No-op; gc runs in the daemon on a 30m schedule`

const versionUsageMsg = `Usage: rex version

Print rex version, commit, and build info.`

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
		if helpRequested(args) {
			fmt.Println(initUsage)
			return nil
		}
		if err := rejectExtraArgs("init", args, initUsage); err != nil {
			return err
		}
		return initflow.Run(bufio.NewReader(os.Stdin), os.Stdout)
	case "serve":
		if helpRequested(args) {
			fmt.Println(serveUsage)
			return nil
		}
		if err := rejectExtraArgs("serve", args, serveUsage); err != nil {
			return err
		}
		return runServe(ctx)
	case "config":
		return runConfig(args)
	case "start", "stop", "restart", "status":
		if helpRequested(args) {
			fmt.Println(daemonControlUsage)
			return nil
		}
		if err := rejectExtraArgs(cmd, args, daemonControlUsage); err != nil {
			return err
		}
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
		if helpRequested(args) {
			fmt.Println(dispatchcmd.Usage)
			return nil
		}
		cfg, err := configcmd.LoadConfig()
		if err != nil {
			return err
		}
		return dispatchcmd.Run(ctx, cfg, args)
	case "callback":
		return runCallback(args)
	case "user":
		// Help can be answered without config; defer LoadConfig so help works
		// before 'rex init'.
		if helpRequested(args) {
			return usercmd.Run(ctx, nil, workspace.Paths{}, args)
		}
		cfg, err := configcmd.LoadConfig()
		if err != nil {
			return err
		}
		ws := workspace.New(mustWorkspaceDir(cfg))
		return usercmd.Run(ctx, cfg, ws, args)
	case "version", "-v", "--version":
		if helpRequested(args) {
			fmt.Println(versionUsageMsg)
			return nil
		}
		if err := rejectExtraArgs("version", args, versionUsageMsg); err != nil {
			return err
		}
		printVersion(os.Stdout)
		return nil
	}
	fmt.Fprintf(os.Stderr, "rex: unknown command '%s'\n\n", cmd)
	fmt.Println(usage)
	os.Exit(1)
	return nil
}

// isHelp reports whether s is one of the accepted help flags.
func isHelp(s string) bool {
	return s == "-h" || s == "--help" || s == "help"
}

// helpRequested returns true when any of args is a help flag. Subcommand
// dispatchers may also call isHelp directly on the first arg.
func helpRequested(args []string) bool {
	for _, a := range args {
		if isHelp(a) {
			return true
		}
	}
	return false
}

// rejectExtraArgs errors if args contains anything other than help flags.
// Use in subcommands that take no arguments — silently dropping a typo
// like `rex session reet` would be worse than failing loudly.
func rejectExtraArgs(cmd string, args []string, usage string) error {
	for _, a := range args {
		if isHelp(a) {
			continue
		}
		return fmt.Errorf("unknown argument for '%s': %s\n%s", cmd, a, usage)
	}
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
	logDir := filepath.Join(cfg.ProjectDir(), "logs")
	w, err := applog.New(logDir, true)
	if err != nil {
		return fmt.Errorf("init log writer: %w", err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
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
	if isHelp(args[0]) {
		fmt.Println(configUsageMsg)
		return nil
	}
	switch args[0] {
	case "setup":
		setupUsage := "Usage: rex config setup\n\nInteractive configuration (writes <project>/config.json)."
		if helpRequested(args[1:]) {
			fmt.Println(setupUsage)
			return nil
		}
		if err := rejectExtraArgs("config setup", args[1:], setupUsage); err != nil {
			return err
		}
		pd, err := configcmd.ProjectDir()
		if err != nil {
			return err
		}
		return configcmd.Setup(bufio.NewReader(os.Stdin), os.Stdout, pd)
	case "show":
		showUsage := "Usage: rex config show\n\nShow current config with secrets masked."
		if helpRequested(args[1:]) {
			fmt.Println(showUsage)
			return nil
		}
		if err := rejectExtraArgs("config show", args[1:], showUsage); err != nil {
			return err
		}
		cfg, err := configcmd.LoadConfig()
		if err != nil {
			return err
		}
		configcmd.Show(os.Stdout, cfg)
		return nil
	case "start", "stop", "restart", "status":
		if helpRequested(args[1:]) {
			fmt.Println(daemonControlUsage)
			return nil
		}
		if err := rejectExtraArgs("config "+args[0], args[1:], daemonControlUsage); err != nil {
			return err
		}
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
		if isHelp(a) {
			fmt.Println(logsUsage)
			return nil
		}
		if a == "-f" {
			follow = true
			continue
		}
		return fmt.Errorf("unknown argument for 'logs': %s\n%s", a, logsUsage)
	}
	cfg, err := configcmd.LoadConfig()
	if err != nil {
		return err
	}
	return configcmd.Logs(cfg, follow)
}

func runSkills(args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	if isHelp(sub) {
		fmt.Println(skillsUsageMsg)
		return nil
	}
	switch sub {
	case "list", "":
		listUsage := "Usage: rex skills list\n\nList workspace skills (name + description)."
		if helpRequested(args[1:]) {
			fmt.Println(listUsage)
			return nil
		}
		if err := rejectExtraArgs("skills list", args[1:], listUsage); err != nil {
			return err
		}
		cfg, err := configcmd.LoadConfig()
		if err != nil {
			return err
		}
		ws := workspace.New(mustWorkspaceDir(cfg))
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
	}
	return fmt.Errorf("unknown skills command: %s", sub)
}

func runTimeline(args []string) error {
	if len(args) == 0 {
		fmt.Println(timelineUsage)
		return nil
	}
	if isHelp(args[0]) {
		fmt.Println(timelineUsage)
		return nil
	}
	subHelp := helpRequested(args[1:])
	switch args[0] {
	case "rebuild":
		if subHelp {
			fmt.Println("Usage: rex timeline rebuild\n\nRebuild the timeline DB from JSONL audit logs.")
			return nil
		}
		if err := rejectExtraArgs("timeline rebuild", args[1:], timelineUsage); err != nil {
			return err
		}
	case "stats":
		if subHelp {
			fmt.Println("Usage: rex timeline stats\n\nShow event-log statistics (count, sessions, time range, cost).")
			return nil
		}
		if err := rejectExtraArgs("timeline stats", args[1:], timelineUsage); err != nil {
			return err
		}
	case "clear":
		if subHelp {
			fmt.Println("Usage: rex timeline clear\n\nClear the events DB. JSONL audit log is untouched.")
			return nil
		}
		if err := rejectExtraArgs("timeline clear", args[1:], timelineUsage); err != nil {
			return err
		}
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
	if len(args) == 0 {
		fmt.Println(sessionUsage)
		return nil
	}
	if isHelp(args[0]) {
		fmt.Println(sessionUsage)
		return nil
	}
	subHelp := helpRequested(args[1:])
	switch args[0] {
	case "reset":
		if subHelp {
			fmt.Println("Usage: rex session reset\n\nReset the main session — a fresh session starts on the next message.")
			return nil
		}
		if err := rejectExtraArgs("session reset", args[1:], sessionUsage); err != nil {
			return err
		}
	case "list":
		if subHelp {
			fmt.Println("Usage: rex session list\n\nList tracked sessions (id, name, last-activity, main marker).")
			return nil
		}
		if err := rejectExtraArgs("session list", args[1:], sessionUsage); err != nil {
			return err
		}
	case "gc":
		if subHelp {
			fmt.Println("Usage: rex session gc\n\nNo-op; the daemon runs gc on a 30m schedule.")
			return nil
		}
		if err := rejectExtraArgs("session gc", args[1:], sessionUsage); err != nil {
			return err
		}
	}
	cfg, err := configcmd.LoadConfig()
	if err != nil {
		return err
	}
	ws := workspace.New(mustWorkspaceDir(cfg))

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
			runner := &claude.Runner{Bin: claude.FindBin(cfg.ClaudeBin), Workdir: ws.Root}
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
	if len(args) == 0 {
		fmt.Println(callbackUsage)
		return nil
	}
	if isHelp(args[0]) {
		fmt.Println(callbackUsage)
		return nil
	}
	subHelp := helpRequested(args[1:])
	switch args[0] {
	case "list":
		if subHelp {
			fmt.Println("Usage: rex callback list\n\nList all registered callbacks.")
			return nil
		}
		if err := rejectExtraArgs("callback list", args[1:], callbackUsage); err != nil {
			return err
		}
	case "remove":
		if subHelp {
			fmt.Println("Usage: rex callback remove <id>\n\nRemove a callback by id.")
			return nil
		}
		if len(args) > 2 {
			return fmt.Errorf("unknown argument for 'callback remove': %s\n%s", args[2], callbackUsage)
		}
	case "create":
		if subHelp {
			fmt.Println(callbackCreateUsage)
			return nil
		}
	}
	cfg, err := configcmd.LoadConfig()
	if err != nil {
		return err
	}
	ws := workspace.New(mustWorkspaceDir(cfg))
	store := callback.NewStore(ws.CallbacksFile)

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

const callbackCreateUsage = `Usage: rex callback create "<prompt>" --schedule "<cron>" | --at "<when>" [options]

Create a callback that runs a prompt later — recurring (--schedule) or once (--at).

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
	if isHelp(args[0]) {
		fmt.Println(callbackCreateUsage)
		return nil
	}
	prompt := args[0]
	args = args[1:]

	var schedule, at, name, command, sessionTarget string
	for i := 0; i < len(args); i++ {
		flag := args[i]
		switch flag {
		case "--schedule", "--at", "--name", "--command", "--session":
		default:
			return fmt.Errorf("unknown flag for 'callback create': %s\n%s", flag, callbackCreateUsage)
		}
		if i+1 >= len(args) {
			return fmt.Errorf("flag %s requires a value\n%s", flag, callbackCreateUsage)
		}
		v := args[i+1]
		i++
		switch flag {
		case "--schedule":
			schedule = v
		case "--at":
			at = v
		case "--name":
			name = v
		case "--command":
			command = v
		case "--session":
			sessionTarget = v
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

// version is the release tag. Override at build time:
//
//	go build -ldflags "-X main.version=v1.2.3" ./cmd/rex
var version = "dev"

func printVersion(w *os.File) {
	commit, dirty, buildTime := buildVCS()
	fmt.Fprintf(w, "rex %s\n", version)
	if commit != "" {
		suffix := ""
		if dirty {
			suffix = " (dirty)"
		}
		fmt.Fprintf(w, "  commit:  %s%s\n", commit, suffix)
	}
	if buildTime != "" {
		fmt.Fprintf(w, "  built:   %s\n", buildTime)
	}
	fmt.Fprintf(w, "  go:      %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func buildVCS() (commit string, dirty bool, buildTime string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, ""
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		case "vcs.time":
			buildTime = s.Value
		}
	}
	return commit, dirty, buildTime
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
