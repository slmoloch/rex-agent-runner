package daemon

import (
	"context"
	"log/slog"
	"strings"

	"github.com/slmoloch/rex-agent-runner/internal/claude"
	"github.com/slmoloch/rex-agent-runner/internal/events"
	"github.com/slmoloch/rex-agent-runner/internal/session"
)

// NoReplyMarker is the sentinel the agent emits as its final turn text to
// tell the daemon to stay silent (e.g. a callback with nothing to report).
// When the agent uses `rex user ...` to deliver a reply, the daemon detects
// that automatically and suppresses the final text without needing this
// marker.
const NoReplyMarker = "NO_REPLY"

// calledRexUser reports whether any turn invoked `rex user text|rich-text|
// voice|file` to deliver content out-of-band. When true, the daemon
// suppresses the final text so the user doesn't get a duplicate message.
func calledRexUser(turns []claude.Turn) bool {
	for _, t := range turns {
		if t.Type != "tool" || t.Name != "Bash" {
			continue
		}
		input, ok := t.Input.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := input["command"].(string)
		if strings.Contains(cmd, "rex user ") {
			return true
		}
	}
	return false
}

// runInSession runs one Claude turn for the given session target, persists
// the event, and returns the response text along with a flag telling the
// caller whether to suppress forwarding the response to the user (set when
// the agent emitted NoReplyMarker in its final text).
func (d *Daemon) runInSession(ctx context.Context, prompt, sessionTarget, trigger, callerSession string) (string, bool) {
	sessionID := d.sessions.Resolve(sessionTarget)

	if sessionID != "" {
		d.sessions.MarkRunning(sessionID)
	}
	result, err := d.runner.Run(ctx, claude.Options{
		Prompt:       prompt,
		SessionID:    sessionID,
		SystemPrompt: d.systemPrompt,
	})
	if sessionID != "" {
		d.sessions.MarkStopped(sessionID)
	}
	if err != nil {
		slog.Error("claude start failed", "err", err)
		return "Error: " + err.Error(), false
	}

	newID := result.SessionID

	// Update the main session pointer only if it still points at the one we
	// just ran — a concurrent /new or daily reset could have wiped it, and
	// we must not resurrect the old id.
	if sessionTarget == session.Main && newID != "" {
		d.sessions.SetMainID(newID, sessionID)
	}
	if newID != "" {
		name := ""
		if sessionTarget == session.Main {
			name = session.Main
		}
		d.sessions.Register(newID, name)
	}

	suppressReply := strings.Contains(result.Response, NoReplyMarker) || calledRexUser(result.Turns)
	response := result.Response
	if suppressReply {
		response = strings.TrimSpace(strings.ReplaceAll(response, NoReplyMarker, ""))
	}

	ev := events.Event{
		Session:                  sessionTarget,
		SessionID:                newID,
		Trigger:                  trigger,
		PromptPreview:            truncate(prompt, 200),
		ResponsePreview:          truncate(response, 500),
		CostUSD:                  result.CostUSD,
		DurationMS:               int(result.Duration.Milliseconds()),
		NumTurns:                 result.NumTurns,
		CallerSession:            callerSession,
		Turns:                    result.Turns,
		InputTokens:              result.InputTokens,
		CacheCreationInputTokens: result.CacheCreationInputTokens,
		CacheReadInputTokens:     result.CacheReadInputTokens,
		OutputTokens:             result.OutputTokens,
		ContextTokens:            result.ContextTokens,
		CacheReadTokens:          result.CacheReadTokens,
	}
	if err := d.events.Append(ev); err != nil {
		slog.Error("events append", "err", err)
	}

	return response, suppressReply
}

// RunJob matches server.JobFunc. It injects caller-session context into the
// prompt so the target agent knows how to reply back.
func (d *Daemon) RunJob(ctx context.Context, prompt, jobName, sessionTarget, callerSession string) (string, error) {
	if callerSession != "" {
		prompt = prompt + "\n\nCaller session ID: " + callerSession +
			"\nTo report results back to the caller, run: rex dispatch " +
			callerSession + " \"<your response>\""
	}
	resp, _ := d.runInSession(ctx, prompt, sessionTarget, jobName, callerSession)
	return resp, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
