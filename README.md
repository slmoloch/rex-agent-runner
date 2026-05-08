# rex — Claude Code Agent Runner

`rex` is a long-running daemon that drives a Claude Code agent on your behalf.
It accepts prompts from a Telegram bot, dispatches scheduled jobs, exposes a
local web dashboard, and tracks every turn in a queryable timeline.

## Features

- **Telegram bot front-end.** Chat with your agent from anywhere; the daemon
  receives updates, runs Claude Code, and replies back. An allow-list pins the
  bot to specific Telegram user IDs.
- **Voice in / voice out.** Optional OpenAI-powered speech-to-text for incoming
  voice notes and text-to-speech replies via `rex user voice`.
- **Local web dashboard.** Embedded UI at `http://127.0.0.1:9821/` for browsing
  events, sessions, and live agent activity, backed by JSON APIs.
- **Persistent sessions.** A "main" session is preserved across messages, with
  additional sessions tracked, listed, reset, and garbage-collected on a 30m
  schedule.
- **Scheduled callbacks.** Recurring (`--schedule "<cron>"`) or one-shot
  (`--at "HH:MM"`, `+5m`, `+2h`, …) prompts. Optional `--command` runs a
  pre-check whose stdout is fed into the prompt; non-zero exit skips the run.
- **Dispatch CLI.** `rex dispatch <session> <message>` sends a prompt into the
  bot pipeline from scripts or other tools.
- **Skills.** Drop reusable instruction packs into the workspace's `skills/`
  folder; `rex skills list` enumerates them for the agent.
- **Timeline store.** Every event is appended to JSONL audit logs and indexed
  in SQLite for fast queries (`rex timeline stats|rebuild|clear`).
- **User channel.** Agents call back into the user via `rex user text|voice|file`
  to surface progress, audio replies, or attachments outside a turn.
- **One-shot project setup.** `rex init` scaffolds a `workspace/` with
  `agent.md` and `memory.md`, records the project dir, and runs interactive
  config.
- **Process-manager integration.** `rex start|stop|restart|status` installs the
  daemon under launchd (macOS) or systemd (Linux) and tails its logs with
  `rex logs -f`.

## Install

```bash
./install.sh                # builds and symlinks rex into ~/.local/bin
./install.sh /usr/local/bin # or pick another install dir
```

Requires Go 1.22+.

## Quick start

```bash
cd <your-project>
rex init                    # scaffold workspace/ and run config setup
rex start                   # install + start the daemon
rex status                  # check it's running; opens dashboard URL
```

## Command reference

```text
Setup:      init
Daemon:     serve | start | stop | restart | status | logs [-f]
Config:     config | config setup | config show
Skills:     skills [list]
Timeline:   timeline stats | rebuild | clear
Sessions:   session reset | list | gc
Dispatch:   dispatch <session> <message>
Callbacks:  callback list | create "<prompt>" --schedule|--at … | remove <id>
User:       user text|voice|file <…>
```

Run `rex help` or any subcommand without arguments for full usage.
