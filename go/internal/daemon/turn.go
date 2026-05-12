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

// rexUserReply is a `rex user <kind> <content>` invocation found inside a
// tool turn. TurnIndex is the position in the original turns slice so the
// UI can correlate replies back to the trace.
type rexUserReply struct {
	Kind      string
	Content   string
	TurnIndex int
}

// scanRexUserReplies walks the turns and returns one entry per `rex user
// text|rich-text|voice|file ...` Bash invocation. The substring match
// mirrors calledRexUser so detection stays consistent between the two paths.
func scanRexUserReplies(turns []claude.Turn) []rexUserReply {
	var out []rexUserReply
	for i, t := range turns {
		if t.Type != "tool" || t.Name != "Bash" {
			continue
		}
		input, ok := t.Input.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := input["command"].(string)
		if !strings.Contains(cmd, "rex user ") {
			continue
		}
		kind, content, ok := parseRexUserCmd(cmd)
		if !ok {
			continue
		}
		out = append(out, rexUserReply{Kind: kind, Content: content, TurnIndex: i})
	}
	return out
}

// parseRexUserCmd pulls (kind, content) out of a shell command that contains
// `rex user <kind> <content>`. Content is best-effort unquoted; complex
// shell constructs (substitution, heredocs) are kept as-is so the UI shows
// something readable rather than nothing.
func parseRexUserCmd(cmd string) (kind, content string, ok bool) {
	const needle = "rex user "
	s := cmd
	for {
		idx := strings.Index(s, needle)
		if idx < 0 {
			return "", "", false
		}
		if idx == 0 || isShellBoundary(s[idx-1]) {
			s = s[idx+len(needle):]
			break
		}
		s = s[idx+1:]
	}
	s = strings.TrimLeft(s, " \t")
	end := strings.IndexAny(s, " \t\n")
	if end < 0 {
		return s, "", true
	}
	kind = s[:end]
	content = stripOuterQuotes(strings.TrimSpace(s[end:]))
	return kind, content, true
}

func isShellBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\n', ';', '|', '&', '(':
		return true
	}
	return false
}

func stripOuterQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if first == last && (first == '"' || first == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// findLastTextTurn returns the index of the last "text" turn in the slice,
// or -1 if there isn't one.
func findLastTextTurn(turns []claude.Turn) int {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Type == "text" {
			return i
		}
	}
	return -1
}

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

	rexReplies := scanRexUserReplies(result.Turns)
	finalSuppressed := strings.Contains(result.Response, NoReplyMarker) || len(rexReplies) > 0
	response := result.Response
	if finalSuppressed {
		response = strings.TrimSpace(strings.ReplaceAll(response, NoReplyMarker, ""))
	}

	replies := make([]events.Reply, 0, len(rexReplies)+1)
	for _, r := range rexReplies {
		replies = append(replies, events.Reply{
			Kind:      r.Kind,
			Content:   r.Content,
			TurnIndex: r.TurnIndex,
		})
	}
	if !finalSuppressed && strings.TrimSpace(response) != "" {
		replies = append(replies, events.Reply{
			Kind:      "final",
			Content:   response,
			TurnIndex: findLastTextTurn(result.Turns),
		})
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
		ReplySent:       len(replies) > 0,
		Replies:         replies,
	}
	if err := d.events.Append(ev); err != nil {
		slog.Error("events append", "err", err)
	}

	return response, finalSuppressed
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
