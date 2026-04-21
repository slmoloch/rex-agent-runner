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
		if d.tryCollectSession(sid, info.Name, info.LastActivity, mainID, "gc") {
			cleaned = append(cleaned, sid)
		}
	}
	return cleaned
}

// gcInstantSession is the eager counterpart to gcOnce: it runs immediately
// after an ephemeral ("new") turn finishes and unregisters the freshly
// created session if nothing references it. Running inline keeps "instant"
// sessions from cluttering sessions.json for up to 30 minutes.
func (d *Daemon) gcInstantSession(sid string) {
	tracked := d.sessions.Tracked()
	info, ok := tracked[sid]
	if !ok {
		return
	}
	mainID := d.sessions.GetMainID()
	d.tryCollectSession(sid, info.Name, info.LastActivity, mainID, "gc-instant")
}

// tryCollectSession applies the shared gc policy to a single session; returns
// true if it was collected.
func (d *Daemon) tryCollectSession(sid, name, lastActivity, mainID, trigger string) bool {
	if sid == "" || sid == mainID {
		return false
	}
	if d.sessions.IsRunning(sid) {
		return false
	}
	if d.dispatchedToActive(sid) {
		return false
	}
	if d.hasCallbacks(sid, mainID) {
		return false
	}
	slog.Info("gc collecting session",
		"id", sid, "name", name, "last_activity", lastActivity, "trigger", trigger)
	d.sessions.Unregister(sid)
	evName := name
	if evName == "" {
		evName = sid
	}
	_ = d.events.Append(events.Event{
		Session:         evName,
		SessionID:       sid,
		Trigger:         trigger,
		PromptPreview:   "Session garbage collected",
		ResponsePreview: "last_activity=" + lastActivity,
	})
	return true
}

func (d *Daemon) dispatchedToActive(sessionID string) bool {
	rows, err := d.timeline.Query(1, "")
	if err != nil {
		return false
	}
	tracked := d.sessions.Tracked()
	for _, e := range rows {
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
