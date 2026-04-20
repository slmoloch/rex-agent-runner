package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/events"
	"github.com/slmoloch/rex-agent-runner/internal/session"
)

const gcInterval = 30 * time.Minute

func (d *Daemon) gcLoop(ctx context.Context) {
	slog.Info("session gc loop started", "interval", gcInterval)
	t := time.NewTicker(gcInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cleaned := d.gcOnce()
			if len(cleaned) > 0 {
				slog.Info("gc cleaned sessions", "count", len(cleaned), "ids", cleaned)
			}
		}
	}
}

// gcOnce mirrors rex_gc.py:collect. A tracked session is removed iff it:
//
//   - is not the main session,
//   - is not currently running a turn,
//   - did not recently dispatch to another still-active session,
//   - and has no callbacks targeting it.
func (d *Daemon) gcOnce() []string {
	tracked := d.sessions.Tracked()
	mainID := d.sessions.GetMainID()
	var cleaned []string

	for sid, info := range tracked {
		if sid == mainID {
			continue
		}
		if d.sessions.IsRunning(sid) {
			continue
		}
		if d.dispatchedToActive(sid) {
			continue
		}
		if d.hasCallbacks(sid, mainID) {
			continue
		}
		slog.Info("gc collecting session",
			"id", sid, "name", info.Name, "last_activity", info.LastActivity)
		d.sessions.Unregister(sid)
		cleaned = append(cleaned, sid)
		name := info.Name
		if name == "" {
			name = sid
		}
		_ = d.events.Append(events.Event{
			Session:         name,
			SessionID:       sid,
			Trigger:         "gc",
			PromptPreview:   "Session garbage collected",
			ResponsePreview: "last_activity=" + info.LastActivity,
		})
	}
	return cleaned
}

func (d *Daemon) dispatchedToActive(sessionID string) bool {
	evs, err := d.events.Load(1)
	if err != nil {
		return false
	}
	tracked := d.sessions.Tracked()
	for _, e := range evs {
		if e.CallerSession != sessionID {
			continue
		}
		target := e.SessionID
		if target == "" || target == sessionID {
			continue
		}
		if _, ok := tracked[target]; ok {
			return true
		}
		if d.sessions.IsRunning(target) {
			return true
		}
	}
	return false
}

func (d *Daemon) hasCallbacks(sessionID, mainID string) bool {
	for _, cb := range d.callbacks.List() {
		target := cb.Session
		if target == "" {
			target = "new"
		}
		if target == sessionID {
			return true
		}
		if target == session.Main && sessionID == mainID {
			return true
		}
	}
	return false
}
