package daemon

import (
	"context"
	"log/slog"

	"github.com/slmoloch/rex-agent-runner/internal/claude"
	"github.com/slmoloch/rex-agent-runner/internal/events"
	"github.com/slmoloch/rex-agent-runner/internal/session"
)

// runInSession is the Go port of rex_bot.py:_run_in_session. It runs one
// Claude turn for the given session target, persists the event, and returns
// the response text along with a flag telling the caller whether the turn
// already delivered a reply via `rex user` (so the fallback text reply
// should be suppressed).
//
// Turns are dispatched through the Pool: sessionTarget=="main" routes to the
// pinned main worker (FIFO, never interleaved), everything else goes onto
// the shared ephemeral queue (DefaultEphemeralWorkers workers).
func (d *Daemon) runInSession(ctx context.Context, prompt, sessionTarget, trigger, callerSession string) (string, bool) {
	sessionID := d.sessions.Resolve(sessionTarget)

	class := claude.ClassEphemeral
	if sessionTarget == session.Main {
		class = claude.ClassMain
	}

	if sessionID != "" {
		d.sessions.MarkRunning(sessionID)
	}
	result, err := d.pool.Run(ctx, class, claude.Options{
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

	usedRexUser := len(result.RexUserSends) > 0
	if usedRexUser {
		modes := make([]string, 0, len(result.RexUserSends))
		for _, s := range result.RexUserSends {
			if m, ok := s["mode"].(string); ok {
				modes = append(modes, m)
			}
		}
		slog.Info("rex user delivered", "count", len(result.RexUserSends), "modes", modes)
	}

	ev := events.Event{
		Session:         sessionTarget,
		SessionID:       newID,
		Trigger:         trigger,
		PromptPreview:   truncate(prompt, 200),
		ResponsePreview: truncate(result.Response, 500),
		CostUSD:         result.CostUSD,
		DurationMS:      int(result.Duration.Milliseconds()),
		NumTurns:        result.NumTurns,
		CallerSession:   callerSession,
		Tools:           result.Tools,
	}
	if usedRexUser {
		ev.RexUserSends = result.RexUserSends
	}
	if err := d.events.Append(ev); err != nil {
		slog.Error("events append", "err", err)
	}

	// "Instant" (sessionTarget=="new") sessions are collected as soon as the
	// turn ends, unless something still references the fresh session id
	// (callbacks, an in-flight dispatch chain, …). Longer-lived sessions are
	// left to the periodic gc loop.
	if sessionTarget == "new" && newID != "" {
		d.gcInstantSession(newID)
	}

	return result.Response, usedRexUser
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
