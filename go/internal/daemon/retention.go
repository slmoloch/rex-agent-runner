package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/events"
)

// JSONLRetention is how long an hourly audit-log file lives on disk.
// The SQLite timeline.db keeps events forever; JSONL is for human audit
// + emergency DB rebuild, so a bounded window is fine.
const JSONLRetention = 30 * 24 * time.Hour

// retentionLoop deletes hourly JSONL files older than JSONLRetention once a
// day. Runs an initial sweep ~1 minute after startup so fresh installs don't
// wait 24h for the first cleanup.
func (d *Daemon) retentionLoop(ctx context.Context) {
	slog.Info("retention loop started", "window", JSONLRetention)

	fire := func() {
		deleted := events.CleanupOlderThan(d.ws.EventsDir, JSONLRetention)
		if len(deleted) > 0 {
			slog.Info("retention: deleted jsonl files", "count", len(deleted))
		}
	}

	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	fire()

	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fire()
		}
	}
}
