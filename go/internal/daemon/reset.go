package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/session"
)

const prepareResetPrompt = "Heads up: your session is about to be reset."

// dailyResetLoop resets the main session every midnight. If the main session
// is busy when midnight hits, it waits until the session becomes idle before
// triggering the reset.
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

		// Preparation prompt on the outgoing session.
		if mainID := d.sessions.GetMainID(); mainID != "" {
			func() {
				cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				_, _ = d.runInSession(cctx, prepareResetPrompt, session.Main, "daily-reset-prepare", "")
			}()
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

		d.resetTopicSessions(ctx)
	}
}

// resetTopicSessions recycles every forum-topic session at the daily reset,
// the same way the main session is recycled. Topics that were active in the
// last day get the heads-up prompt first; dormant ones are just cleared, so
// a forum with many topics doesn't cost one LLM call per topic per night.
// No seeding turn is run: the next message in a topic starts its session
// with the topic context attached.
func (d *Daemon) resetTopicSessions(ctx context.Context) {
	for target, topic := range d.sessions.Topics() {
		if topic.SessionID == "" {
			continue
		}
		if d.sessions.IsRunning(topic.SessionID) {
			slog.Info("daily reset: topic session is active, skipping", "topic", target)
			continue
		}
		if recentlyActive(topic.LastActivity) {
			func() {
				cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				_, _ = d.runInSession(cctx, prepareResetPrompt, target, "daily-reset-prepare", "")
			}()
		}
		d.sessions.Reset(target)
		slog.Info("daily reset: topic session reset", "topic", target, "name", topic.Name)
	}
}

// recentlyActive reports whether an RFC3339 timestamp is within the last day.
// An unparseable or missing stamp counts as stale.
func recentlyActive(stamp string) bool {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return false
	}
	return time.Since(t) < 24*time.Hour
}
