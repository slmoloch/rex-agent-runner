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
// tell the daemon not to forward anything to the user. Use it after already
// delivering a reply via `rex user`, or in callbacks/jobs that don't need
// to say anything to the user.
const NoReplyMarker = "NO_REPLY"

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

	suppressReply := strings.Contains(result.Response, NoReplyMarker)
	response := result.Response
	if suppressReply {
		response = strings.TrimSpace(strings.ReplaceAll(response, NoReplyMarker, ""))
	}

	ev := events.Event{
		Session:         sessionTarget,
		SessionID:       newID,
		Trigger:         trigger,
		PromptPreview:   truncate(prompt, 200),
		ResponsePreview: truncate(response, 500),
		CostUSD:         result.CostUSD,
		DurationMS:      int(result.Duration.Milliseconds()),
		NumTurns:        result.NumTurns,
		CallerSession:   callerSession,
		Turns:           result.Turns,
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
