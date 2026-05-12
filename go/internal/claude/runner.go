// Package claude runs the Claude Code CLI as a subprocess, parses its
// stream-json output, and enforces idle / total timeouts.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// DefaultIdleTimeout matches the Python implementation: if no line of output
// arrives for this long, we assume Claude is stuck and kill it.
const DefaultIdleTimeout = 10 * time.Minute

// Options configures a single run.
type Options struct {
	Prompt       string
	SessionID    string        // resume an existing session; mutually exclusive with SystemPrompt
	SystemPrompt string        // used only when SessionID is empty
	IdleTimeout  time.Duration // 0 => DefaultIdleTimeout
	MaxTimeout   time.Duration // 0 => unlimited
}

// Result is everything the caller gets back from a run.
type Result struct {
	Response  string        `json:"response"`
	SessionID string        `json:"session_id"`
	CostUSD   float64       `json:"cost_usd"`
	Duration  time.Duration `json:"duration_ms"`
	NumTurns  int           `json:"num_turns"`
	Turns     []Turn        `json:"turns"`
}

// Runner executes Claude Code. It is immutable after construction; a single
// Runner can serve many concurrent Run calls.
type Runner struct {
	Bin     string // path to the claude CLI
	Workdir string // cwd for the subprocess
}

// Run launches one turn. The returned error is non-nil only for "we couldn't
// even start talking to Claude" cases; protocol-level failures (non-zero exit,
// timeout) are encoded into Result.Response and logged.
func (r *Runner) Run(ctx context.Context, opts Options) (*Result, error) {
	if r.Bin == "" {
		return nil, errors.New("claude: Runner.Bin is empty")
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}

	args := []string{
		"-p", opts.Prompt,
		"--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions",
		"--disallowedTools", "CronCreate,CronDelete,CronList,TaskCreate,TaskGet,TaskList,TaskUpdate,TodoWrite",
	}
	if opts.SessionID != "" {
		args = append(args, "-r", opts.SessionID)
	} else if opts.SystemPrompt != "" {
		dateLine := fmt.Sprintf("\n\n## Current Date\n\nToday is %s.",
			time.Now().Format("2006-01-02"))
		args = append(args, "--system-prompt", opts.SystemPrompt+dateLine)
	}

	// Total-timeout is enforced by deriving a child context; idle-timeout is
	// enforced by a watchdog goroutine that cancels this same ctx on starvation.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.MaxTimeout > 0 {
		var stop context.CancelFunc
		runCtx, stop = context.WithTimeout(runCtx, opts.MaxTimeout)
		defer stop()
	}

	cmd := exec.CommandContext(runCtx, r.Bin, args...)
	cmd.Dir = r.Workdir
	cmd.Env = os.Environ()
	if opts.SessionID != "" {
		cmd.Env = append(cmd.Env, "REX_SESSION_ID="+opts.SessionID)
	}

	// Kill the whole process group on timeout so tools spawned by Claude die too.
	setPgid(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	slog.Info("claude start", "prompt_preview", truncate(opts.Prompt, 200),
		"session", opts.SessionID)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", r.Bin, err)
	}

	var (
		state        runState
		stderrBuf    []byte
		stderrMu     sync.Mutex
		wg           sync.WaitGroup
		lastActivity atomic_time
	)
	state.sessionID = opts.SessionID
	lastActivity.Store(time.Now())

	wg.Add(2)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			lastActivity.Store(time.Now())
			parseLine(sc.Bytes(), &state)
		}
	}()
	go func() {
		defer wg.Done()
		// stderr is usually small; buffer it for error reporting.
		buf, _ := io.ReadAll(stderr)
		stderrMu.Lock()
		stderrBuf = buf
		stderrMu.Unlock()
		if len(buf) > 0 {
			lastActivity.Store(time.Now())
		}
	}()

	// Idle watchdog.
	idleExceeded := make(chan time.Duration, 1)
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		tick := time.NewTicker(minDur(5*time.Second, maxDur(time.Second, idle/10)))
		defer tick.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-tick.C:
				since := time.Since(lastActivity.Load())
				if since > idle {
					select {
					case idleExceeded <- since:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	waitErr := cmd.Wait()
	wg.Wait()
	cancel()
	<-watchdogDone

	// Timeout paths.
	select {
	case since := <-idleExceeded:
		reason := fmt.Sprintf("no output for %s (idle_timeout=%s)",
			since.Round(time.Second), idle)
		slog.Error("claude killed", "reason", reason)
		return state.toResult("Error: Claude killed (" + reason + ")."), nil
	default:
	}
	if opts.MaxTimeout > 0 && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		reason := fmt.Sprintf("exceeded max timeout of %s", opts.MaxTimeout)
		slog.Error("claude killed", "reason", reason)
		return state.toResult("Error: Claude killed (" + reason + ")."), nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return state.toResult("Error: Claude canceled."), nil
	}

	if waitErr != nil {
		stderrMu.Lock()
		serr := string(stderrBuf)
		stderrMu.Unlock()
		slog.Error("claude exit", "err", waitErr, "stderr", truncate(serr, 500))
		msg := serr
		if msg == "" {
			msg = waitErr.Error()
		}
		return state.toResult("Error: " + msg), nil
	}

	slog.Info("claude done",
		"turns", state.numTurns,
		"duration_ms", state.durationMS,
		"cost_usd", state.costUSD)
	return state.toResult(state.responseText), nil
}

type runState struct {
	responseText string
	sessionID    string
	costUSD      float64
	numTurns     int
	durationMS   int
	turns        []Turn
}

func (s *runState) toResult(responseOverride string) *Result {
	return &Result{
		Response:  responseOverride,
		SessionID: s.sessionID,
		CostUSD:   s.costUSD,
		Duration:  time.Duration(s.durationMS) * time.Millisecond,
		NumTurns:  s.numTurns,
		Turns:     s.turns,
	}
}

// Per-string clip caps. Tool inputs are clipped per-field rather than as a
// blob so the resulting JSON is always parseable; text turns are clipped as a
// whole because they're free-form prose.
const (
	maxToolStringChars = 1000
	maxTextTurnChars   = 4000
)

func parseLine(line []byte, s *runState) {
	if len(line) == 0 {
		return
	}
	var ev Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	switch ev.Type {
	case "result":
		s.responseText = ev.Result
		if ev.SessionID != "" {
			s.sessionID = ev.SessionID
		}
		s.costUSD = ev.TotalCostUSD
		s.numTurns = ev.NumTurns
		s.durationMS = ev.DurationMS
	case "assistant":
		if ev.Message == nil {
			return
		}
		for _, b := range ev.Message.Content {
			switch b.Type {
			case "tool_use":
				input := decodeAndClipToolInput(b.Input, maxToolStringChars)
				s.turns = append(s.turns, Turn{Type: "tool", Name: b.Name, Input: input})
				slog.Info("claude tool", "name", b.Name, "input", truncate(toolInputPreview(input), 200))
			case "text":
				if b.Text == "" {
					continue
				}
				text := clipString(b.Text, maxTextTurnChars)
				s.turns = append(s.turns, Turn{Type: "text", Text: text})
				slog.Info("claude text", "preview", truncate(b.Text, 300))
			case "thinking":
				if b.Thinking == "" {
					continue
				}
				text := clipString(b.Thinking, maxTextTurnChars)
				s.turns = append(s.turns, Turn{Type: "thinking", Text: text})
				slog.Info("claude thinking", "preview", truncate(b.Thinking, 300))
			}
		}
	}
}

// decodeAndClipToolInput parses raw tool-use input JSON into a Go value and
// clips every string leaf that exceeds max. If the raw bytes don't parse
// (shouldn't happen for well-formed Claude output), it falls back to a
// single-key object containing the clipped raw text so the resulting JSON
// is still valid.
func decodeAndClipToolInput(raw json.RawMessage, max int) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]any{"_raw": clipString(string(raw), max)}
	}
	return clipJSONStrings(v, max)
}

// clipJSONStrings walks an arbitrary JSON value and returns a copy where any
// string leaf longer than max is shortened with an ellipsis suffix. Maps,
// slices and primitives are returned as-is otherwise.
func clipJSONStrings(v any, max int) any {
	switch x := v.(type) {
	case string:
		return clipString(x, max)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = clipJSONStrings(val, max)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = clipJSONStrings(val, max)
		}
		return out
	default:
		return v
	}
}

// clipString shortens s to at most max bytes (rune-safe) and appends an
// ellipsis so callers can tell the original was longer.
func clipString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Trim to a rune boundary so the output is always valid UTF-8.
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func toolInputPreview(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// FindBin mirrors the _find_claude() probe order in claude_runner.py.
func FindBin(configured string) string {
	if configured != "" {
		return configured
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(home, ".claude", "local", "claude"),
		"/usr/local/bin/claude",
		"/opt/homebrew/bin/claude",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "claude"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// atomic_time is a tiny atomic wrapper so the watchdog and scanner goroutines
// can race-freely share the last-activity timestamp.
type atomic_time struct {
	mu sync.Mutex
	t  time.Time
}

func (a *atomic_time) Store(t time.Time) { a.mu.Lock(); a.t = t; a.mu.Unlock() }
func (a *atomic_time) Load() time.Time   { a.mu.Lock(); t := a.t; a.mu.Unlock(); return t }
