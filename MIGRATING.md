# Migrating from the Python version

The Go rewrite reads the same `config.json`, `workspace/`, and `events.db`
as the Python version — on-disk state is preserved. What needs manual
cleanup is anything the Python tree installed **outside** the repo:
launchd plists and the virtualenv.

Run these steps once, from the repo clone.

## macOS

Skip step 3 if you never scheduled any callbacks.

```sh
# 1. Stop the Python daemon (while bin/rex still points at the old shell).
launchctl unload ~/Library/LaunchAgents/com.rex.agent-runner.plist 2>/dev/null || true
rm -f ~/Library/LaunchAgents/com.rex.agent-runner.plist

# 2. Remove the virtualenv from your project directory.
rm -rf "$(cat ~/.config/rex/project_dir)/venv"

# 3. Unload every per-callback plist. The Go scheduler runs inside the
# daemon; no plists needed anymore.
for p in ~/Library/LaunchAgents/com.rex.callback.*.plist; do
    [ -e "$p" ] || continue
    launchctl unload "$p" 2>/dev/null || true
    rm -f "$p"
done

# 4. Pull the latest code and rebuild.
git pull
./install.sh

# 5. Install the Go-native launchd unit. This writes a new plist pointing
# at `bin/rex serve` instead of the Python entrypoint.
rex config start
```

Your `workspace/sessions.json`, `workspace/callbacks.json`,
`workspace/events.db`, AGENT.md, MEMORY.md, and skills/ stay intact. Any
recurring callbacks you created via `rex callback create --schedule ...`
are still in `callbacks.json` and will fire on their usual cron once the
new daemon is running.

## Linux

The Python version never shipped a systemd unit, so there's nothing to
unload. Just:

```sh
rm -rf "$(cat ~/.config/rex/project_dir)/venv"
git pull
./install.sh
rex config start   # creates ~/.config/systemd/user/rex-agent-runner.service
```

## Verifying

```sh
rex config status
rex timeline stats    # should report your existing events
rex callback list     # should show any pre-existing callbacks
```

If `rex timeline stats` reports 0 events but you have hourly JSONL files
under `workspace/events/`, run `rex timeline rebuild` — it'll repopulate
the SQLite DB from the audit log.
