# Python → Go port: cleanup plan

The Go rewrite under `go/` is now feature-complete. This document is the
playbook for retiring the Python implementation.

## 1. What changed vs. the Python version

Three deliberate simplifications made the Go port cleaner and native on Linux:

- **No launchd plists for callbacks.** `rex_callback.py` wrote one launchd
  plist per callback so they'd fire even when the daemon was down. The Go
  port runs a scheduler goroutine inside the daemon — callbacks only fire
  while the daemon is running. In return, callbacks are now cross-platform
  (macOS and Linux) with a single code path.
- **No SQLite timeline.** `rex_timeline.py` mirrored events into `events.db`.
  The Go port reads the hourly JSONL files (which were already the source of
  truth) directly. `rex timeline stats` now walks the JSONL; `rebuild` is a
  no-op; `clear` prints the `rm` to run yourself.
- **No external Go dependencies.** No `python-telegram-bot` equivalent, no
  `openai` SDK, no `aiohttp`. The Telegram bot, HTTP dashboard, and OpenAI
  STT/TTS are all implemented on stdlib `net/http`. `go.mod` has zero require
  entries.

Daemon process management still relies on the OS: `rex start` writes a
launchd plist on macOS, a systemd user unit on Linux. Both are generated on
demand — there's no plist in the repo.

## 2. Port completion checklist

All modules ported:

- [x] `internal/config` — `config.json` load/save
- [x] `internal/claude` — stream-json CLI runner with idle watchdog
- [x] `internal/voice` — OpenAI STT/TTS (stdlib `net/http`)
- [x] `internal/workspace` — shared workspace path layout
- [x] `internal/skills` — SKILL.md loader
- [x] `internal/events` — JSONL append + queries
- [x] `internal/session` — session store + running/tracked registries
- [x] `internal/cron` — five-field parser compatible with `rex_callback.py`
- [x] `internal/callback` — store + in-daemon scheduler
- [x] `internal/telegram` — Bot API client (sendMessage/Voice/Document, long poll)
- [x] `internal/server` — embedded dashboard, `/api/events`, `/api/sessions`, `/job`
- [x] `internal/daemon` — bot polling, gc loop, daily reset, scheduler orchestration
- [x] `internal/configcmd` — config show/setup + daemon start/stop/restart/status/logs
- [x] `internal/initflow` — `rex init`
- [x] `internal/usercmd` — `rex user {text|voice|file}` (marker file writes)
- [x] `internal/dispatchcmd` — `rex dispatch`
- [x] `cmd/rex` — every subcommand wired

Verified:
- `go build ./...` and `go vet ./...` clean on `linux/amd64`.
- Cross-compiles clean to `darwin/arm64`.
- `rex init`, `rex config show`, `rex skills list`, `rex session list`,
  `rex callback {list,create,remove}`, `rex timeline stats`, `rex dispatch`
  (error path) smoke-tested.

## 3. Switch the installed command over

Replace the shell wrapper with the Go binary so `rex` invokes native code
directly:

```sh
# From the project root
rm bin/rex                       # old shell dispatcher
mv bin/rex-go bin/rex            # Go binary takes the canonical name
```

Update `install.sh` at the same time:

- Delete the venv section (`python3 -m venv`, `pip install`).
- Delete the `chmod +x "$BIN_DIR/rex"` line (Go build already produces an
  executable).
- Change the Go build output from `bin/rex-go` to `bin/rex`.
- Make `go` a hard requirement: drop the `command -v go` guard and fail with
  a clear message if Go is missing.

After editing, `install.sh` should be roughly: resolve paths → `go build -o
bin/rex ./cmd/rex` → symlink into `$INSTALL_DIR`.

## 4. Migrate users' daemon unit

Python's `rex start` wrote a launchd plist with
`ProgramArguments=["venv/bin/python", "rex_bot.py"]`. The Go binary writes a
plist that points at `bin/rex serve` and, on Linux, a systemd user unit that
does the same. Any existing user must run `rex restart` once after
upgrading so the new unit overwrites the old one.

On Linux there was no daemon unit before — the Go port generates
`~/.config/systemd/user/rex-agent-runner.service` automatically.

## 5. Delete the Python tree

```sh
# Source files
rm claude_runner.py \
   rex_bot.py rex_callback.py rex_config.py rex_dispatch.py \
   rex_events.py rex_gc.py rex_init.py rex_session.py \
   rex_skills.py rex_timeline.py rex_user.py rex_whisper.py

# Runtime + deps
rm -rf venv/
rm requirements.txt

# Old shell dispatcher
rm bin/rex   # (unless already replaced per step 3)

# Web assets and system prompt now live inside the Go binary via //go:embed
# — the repo-root copies are no longer referenced.
rm -rf web/
rm rex_system_prompt.md
```

The workspace state directory (`workspace/`, `workspace/.rex/`,
`workspace/events/`, `workspace/sessions.json`, `workspace/callbacks.json`)
stays — the Go implementation reads the same on-disk format.

One-time migration for existing workspaces: if `workspace/events.db` exists
from the Python era, you can delete it — the Go port never touches it.

## 6. CI / tooling

- Remove any GitHub Actions step that runs `pip install`, `pytest`, `ruff`,
  or `mypy` against the Python sources.
- Add `go build ./... && go vet ./... && go test ./...`, ideally with a
  matrix over `{darwin, linux} × {amd64, arm64}`.
- The top-level README should drop Python 3.x as a prerequisite and add Go
  (1.22+) in its place.

## 7. Post-cleanup sanity checks

```sh
# No Python left in the tree
find . -name '*.py' -not -path './go/*' -not -path './workspace/*'

# Binary builds for both targets
cd go
GOOS=darwin  GOARCH=arm64 go build -o /tmp/rex-darwin-arm64 ./cmd/rex
GOOS=linux   GOARCH=amd64 go build -o /tmp/rex-linux-amd64  ./cmd/rex

# End-to-end: fresh install, init, send text and voice, verify dashboard
./install.sh
rex init
rex config setup
rex start
open http://127.0.0.1:9821/
# ... then test via Telegram
```

## 8. Known post-port follow-ups (not blockers)

- **Log rotation.** The Python daemon rotated `logs/rex.log` hourly with 168
  backups. The Go daemon logs to stderr (captured by launchd / systemd); add
  external rotation (`logrotate` on Linux) or a rotating writer if log size
  becomes an issue.
- **In-daemon scheduling means callbacks skip firing while the daemon is
  down.** If a scheduled time falls inside a downtime window, that
  occurrence is lost; the recurring callback fires next time it matches.
  Users who need launchd-style "wake me up" behavior should schedule via
  the OS's cron / launchd directly.
