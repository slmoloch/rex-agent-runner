# Migrating from the Python version

The Go rewrite reads the same `config.json`, `workspace/`, and `events.db`
as the Python version — on-disk state is preserved. What needs cleanup
is the launchd plists (macOS) and the virtualenv the Python installer
created. `rex migrate-from-python` takes care of both.

```sh
# From the repo clone:
git pull
./install.sh
rex migrate-from-python
rex config start
```

What `migrate-from-python` does:

- **macOS**: unloads and removes `~/Library/LaunchAgents/com.rex.agent-runner.plist`
  (the Python daemon) and every `~/Library/LaunchAgents/com.rex.callback.*.plist`
  (callbacks now run inside the daemon, so no per-callback plists are needed).
- **Linux**: nothing to do on the launchd side — the Python version never
  shipped a systemd unit.
- **Both**: removes `<project>/venv` if it exists.

Then `rex config start` writes the new native daemon unit (launchd plist
on macOS, `~/.config/systemd/user/rex-agent-runner.service` on Linux).

## What carries over automatically

`config.json`, `workspace/sessions.json`, `workspace/callbacks.json`,
`workspace/events.db`, `AGENT.md`, `MEMORY.md`, and `skills/` keep
working — both implementations share the on-disk format. Any recurring
callbacks in `callbacks.json` will fire on their usual cron once the new
daemon is running.

## Verifying

```sh
rex config status
rex timeline stats    # should report your existing events
rex callback list     # should show any pre-existing callbacks
```

If `rex timeline stats` reports 0 events but you have hourly JSONL files
under `workspace/events/`, run `rex timeline rebuild` — it repopulates
the SQLite DB from the audit log.

---

> **Note:** `rex migrate-from-python` is a transitional subcommand. It
> will be removed on 2026-10-01, once everyone has migrated off the
> Python version. After that, the command will no longer exist; if you
> haven't migrated by then, run the steps manually from the commit
> history of this file.
