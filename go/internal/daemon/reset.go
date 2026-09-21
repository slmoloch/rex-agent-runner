package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/session"
)

// PrepareResetPrompt builds the last turn a conversation gets before it is
// discarded — the daily memory sweep. The session is about to lose its
// history, so this is the moment to write down anything worth keeping.
//
// Where that goes is deliberately not named here: memory layout belongs to
// the workspace (AGENT.md, a memory skill, the MEMORY.md `rex init`
// scaffolds), and hardcoding a path would override whatever the user set up.
//
// topicName may be empty; it is used to attribute what the sweep writes, so
// memories from a forum topic don't read as if everything happened in one
// conversation.
func PrepareResetPrompt(sessionTarget, topicName string) string {
	prompt := "Heads up: this session is about to be reset. This is your last turn with its " +
		"history — after it, the conversation is gone.\n\n"
	if session.IsTopicTarget(sessionTarget) {
		prompt += "It serves the Telegram forum topic " + describeTopic(sessionTarget, topicName) +
			". Attribute what you write to that topic.\n\n"
	}
	prompt += "Sweep the conversation into memory now, wherever your instructions say memory " +
		"lives and in the layout they define: decisions we reached, facts about the user, " +
		"anything still open and what happens next. Merge with what is already there rather " +
		"than appending duplicates, and leave out small talk. If nothing is worth keeping, " +
		"change nothing.\n\n" +
		"Don't message the user about this."
	return prompt
}

// describeTopic names a topic for a prompt: its title when Telegram has told
// us one, always with the target so the agent can address it later.
func describeTopic(target, name string) string {
	if name == "" {
		return target
	}
	return fmt.Sprintf("%q (%s)", name, target)
}

// dailyResetLoop resets every conversation at midnight — the main session
// and one per forum topic. If the main session is busy when midnight hits,
// it waits until the session becomes idle before triggering the reset.
func (d *Daemon) dailyResetLoop(ctx context.Context) {
	slog.Info("daily reset scheduler started")
	for {
		now := time.Now()
		tomorrow := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(24 * time.Hour)
		wait := time.Until(tomorrow)
		slog.Info("daily reset scheduled", "at", tomorrow, "in", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		// Wait until the current main session is no longer running.
		for {
			mainID := d.sessions.GetMainID()
			if mainID == "" || !d.sessions.IsRunning(mainID) {
				break
			}
			slog.Info("daily reset: main session is active, waiting")
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
		}

		// Topics are swept first so the main session, restarted below, reads
		// a memory file that already carries the day's topic conversations.
		d.resetTopicSessions(ctx)

		// Memory sweep on the outgoing main session.
		if mainID := d.sessions.GetMainID(); mainID != "" {
			d.sweep(ctx, session.Main, "")
		}

		d.sessions.ResetMain()
		slog.Info("daily reset: main session reset")

		// Seed the new session with a heads-up note.
		today := time.Now().Format("2006-01-02")
		initPrompt := "Your session has been reset (daily midnight reset). Today's date is " + today + "."
		func() {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			_, _ = d.runInSession(cctx, initPrompt, session.Main, "daily-reset", "")
		}()
	}
}

// resetTopicSessions sweeps and recycles the forum topics that were talked
// in since the last reset, one at a time — the sweeps write to the same
// memory files, so they must not run concurrently. No seeding turn is run: the
// next message in a topic starts its session with the topic context
// attached.
func (d *Daemon) resetTopicSessions(ctx context.Context) {
	topics := d.sessions.Topics()
	sweep, busy := topicsToSweep(topics, d.sessions.IsRunning)
	for _, target := range busy {
		slog.Info("daily reset: topic session is active, leaving it for the next sweep",
			"topic", target, "name", topics[target].Name)
	}
	for _, target := range sweep {
		d.sweep(ctx, target, topics[target].Name)
		d.sessions.Reset(target)
		slog.Info("daily reset: topic session reset", "topic", target, "name", topics[target].Name)
	}
}

// sweep runs one memory-sweep turn in the given conversation.
func (d *Daemon) sweep(ctx context.Context, sessionTarget, topicName string) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, _ = d.runInSession(cctx, PrepareResetPrompt(sessionTarget, topicName),
		sessionTarget, "daily-reset-prepare", "")
}

// topicsToSweep splits the known topics into the ones to sweep now and the
// ones to leave alone. A topic is swept iff it still holds a live session:
// a reset clears that pointer, so a session id surviving until midnight is
// exactly a topic that was discussed since the last sweep — and one whose
// context is about to be discarded. Topics mid-turn are left for the next
// sweep; their session, and so its history, lives on, so nothing is lost.
// Both lists are sorted, which keeps a night's sweeps in a stable order.
func topicsToSweep(topics map[string]session.Topic, isRunning func(string) bool) (sweep, busy []string) {
	for target, topic := range topics {
		switch {
		case topic.SessionID == "":
			continue
		case isRunning(topic.SessionID):
			busy = append(busy, target)
		default:
			sweep = append(sweep, target)
		}
	}
	sort.Strings(sweep)
	sort.Strings(busy)
	return sweep, busy
}
