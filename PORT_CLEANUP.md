# Python → Go port: cleanup plan

This document describes how to retire the Python implementation **once the Go
port is feature-complete**. Do not run these steps yet — most subcommands
still dispatch to Python via `bin/rex`.

## 1. Port completion checklist

Do not start cleanup until all of these are true:

- [x] `internal/config` — config.json load/save
- [x] `internal/claude` — stream-json CLI runner
- [x] `internal/voice` — OpenAI STT/TTS
- [ ] `internal/session` — session registry + gc (`rex_session.py`, `rex_gc.py`)
- [ ] `internal/dispatch` — job dispatch + event log (`rex_dispatch.py`, `rex_events.py`)
- [ ] `internal/callback` — callback store + runner (`rex_callback.py`)
- [ ] `internal/user` — per-user state / ACLs (`rex_user.py`)
- [ ] `internal/skills` — workspace skills loader (`rex_skills.py`)
- [ ] `internal/timeline` — SQLite timeline + dashboard feed (`rex_timeline.py`)
- [ ] `internal/telegram` — Telegram bot side of `rex_bot.py`
- [ ] `internal/http` — HTTP server + embedded `web/` dashboard
- [ ] `internal/initflow` — `rex init` (`rex_init.py`)
- [ ] `cmd/rex` — all subcommands: `init`, `start|stop|restart|status|logs`,
      `config [setup|show]`, `skills`, `timeline`, `session`, `dispatch`,
      `callback`, `user`
- [ ] Parity test: run the Go daemon against a real Telegram chat and verify
      text, voice in, voice out, dashboard, and callbacks all work

## 2. Switch the installed command over

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
- Make `go` a hard requirement, not a soft one — remove the `command -v go`
  guard and fail with a clear message if Go is missing.

After editing, `install.sh` should be roughly: resolve paths → `go build -o
bin/rex ./cmd/rex` → symlink into `$INSTALL_DIR`.

## 3. Migrate the launchd plist (macOS)

`rex_config.py`'s `_build_plist()` points `ProgramArguments` at
`venv/bin/python rex_bot.py`. The Go `rex config start` should write a plist
that runs the native binary instead:

```xml
<key>ProgramArguments</key>
<array>
    <string>/path/to/install/bin/rex</string>
    <string>serve</string>
</array>
```

Existing users must re-run `rex config restart` after upgrading so the plist
gets rewritten. On Linux, add an equivalent systemd user unit under
`~/.config/systemd/user/rex.service` — the Python code never had this, so it's
new territory.

## 4. Delete the Python tree

```sh
# Source files
rm claude_runner.py \
   rex_bot.py rex_callback.py rex_config.py rex_dispatch.py \
   rex_events.py rex_gc.py rex_init.py rex_session.py \
   rex_skills.py rex_timeline.py rex_user.py rex_whisper.py

# Runtime + deps
rm -rf venv/
rm requirements.txt

# The system prompt moves into the Go binary via //go:embed; once that's done:
rm rex_system_prompt.md
```

The `web/` directory (timeline.html, d3) stays — it's embedded into the Go
binary via `//go:embed web/*` at build time. The `workspace/.rex/`
state directory (sessions, turn markers, timeline SQLite) also stays, since
the Go implementation reads the same on-disk format.

## 5. CI / tooling

- Remove any GitHub Actions step that runs `pip install`, `pytest`, `ruff`,
  or `mypy` against the Python sources.
- Add a `go build ./... && go vet ./... && go test ./...` step in their
  place, ideally with a matrix over `{darwin, linux} × {amd64, arm64}`.
- The `install.sh` README / top-level README should drop any mention of
  Python 3.x as a prerequisite.

## 6. Post-cleanup sanity checks

```sh
# No Python left in the tree
find . -name '*.py' -not -path './go/*' -not -path './workspace/*'

# Binary runs on both targets
GOOS=darwin  GOARCH=arm64 go build -o /tmp/rex-darwin-arm64 ./go/cmd/rex
GOOS=linux   GOARCH=amd64 go build -o /tmp/rex-linux-amd64  ./go/cmd/rex

# End-to-end: fresh install, init, send a text and a voice message
./install.sh
rex init
rex config setup
rex start
# ... then test via Telegram
```
