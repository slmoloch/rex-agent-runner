package callback

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/cron"
)

// Submitter runs a prompt for a named callback/session. The daemon supplies
// this so the scheduler doesn't depend on the daemon package directly.
type Submitter interface {
	Submit(ctx context.Context, prompt, jobName, sessionTarget string)
}

// Run starts the scheduler until ctx is cancelled. It ticks every minute
// (aligned to wall-clock minute boundaries so cron fires don't drift).
func Run(ctx context.Context, store *Store, workdir string, submit Submitter) {
	slog.Info("callback scheduler started")
	lastFired := map[string]time.Time{}
	// Sleep to the next minute boundary so we evaluate at :00.
	now := time.Now()
	delay := time.Until(now.Truncate(time.Minute).Add(time.Minute))
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	for {
		tick(ctx, store, workdir, submit, lastFired, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func tick(ctx context.Context, store *Store, workdir string, submit Submitter,
	lastFired map[string]time.Time, now time.Time,
) {
	minute := now.Truncate(time.Minute)
	for _, cb := range store.List() {
		fire := false
		switch {
		case cb.Recurring:
			sched, err := cron.Parse(cb.Schedule)
			if err != nil {
				slog.Warn("callback has invalid schedule", "id", cb.ID, "err", err)
				continue
			}
			if sched.Matches(minute) && !lastFired[cb.ID].Equal(minute) {
				fire = true
				lastFired[cb.ID] = minute
			}
		default:
			at, err := time.ParseInLocation("2006-01-02 15:04", cb.At, time.Local)
			if err != nil {
				slog.Warn("callback has invalid at", "id", cb.ID, "err", err)
				continue
			}
			if !at.After(now) {
				fire = true
			}
		}
		if !fire {
			continue
		}
		go execute(ctx, store, workdir, submit, cb)
	}
}

// execute mirrors cmd_execute() in rex_callback.py: optional pre-check command,
// then submit, then delete the callback if it was one-shot.
func execute(ctx context.Context, store *Store, workdir string, submit Submitter, cb *Callback) {
	prompt := "This is a scheduled message. Process the following message or request. " +
		"Use `rex user text` if the user needs to be notified. Keep quiet otherwise.\n\n" +
		cb.Prompt

	if cb.Command != "" {
		slog.Info("callback pre-check", "id", cb.ID, "cmd", cb.Command)
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(cctx, "bash", "-c", cb.Command).Output()
		if err != nil {
			slog.Info("callback pre-check failed, skipping", "id", cb.ID, "err", err)
			cleanupOneShot(store, cb)
			return
		}
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			prompt = prompt + "\n\nPre-check command output:\n```\n" + trimmed + "\n```"
		}
	}

	session := cb.Session
	if session == "" {
		session = "new"
	}
	submit.Submit(ctx, prompt, cb.ID, session)
	cleanupOneShot(store, cb)
}

func cleanupOneShot(store *Store, cb *Callback) {
	if cb.Recurring {
		return
	}
	if err := store.Remove(cb.ID); err != nil {
		slog.Warn("callback cleanup failed", "id", cb.ID, "err", err)
	}
}
