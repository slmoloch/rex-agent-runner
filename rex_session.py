#!/usr/bin/env python3
"""rex session - manage persistent Claude sessions."""
from __future__ import annotations

import json
import os
import sys
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR

with open(BASE_DIR / "config.json") as f:
    CONFIG = json.load(f)

WORKDIR = Path(CONFIG.get("workspace", "./workspace")).resolve()
SESSIONS_FILE = WORKDIR / "sessions.json"

MAIN_SESSION = "main"


def load_sessions() -> dict:
    if SESSIONS_FILE.exists():
        with open(SESSIONS_FILE) as f:
            return json.load(f)
    return {}


def save_sessions(sessions: dict) -> None:
    with open(SESSIONS_FILE, "w") as f:
        json.dump(sessions, f, indent=2)
        f.write("\n")


def get_session_id(name: str) -> str | None:
    """Get the Claude session ID for a named session. Returns None if not yet created."""
    sessions = load_sessions()
    return sessions.get(name)


def set_session_id(name: str, session_id: str) -> None:
    """Store or update a Claude session ID for a named session."""
    sessions = load_sessions()
    sessions[name] = session_id
    save_sessions(sessions)


def reset_session(name: str) -> None:
    """Reset a session so it creates a fresh one on next use."""
    sessions = load_sessions()
    if name in sessions:
        sessions[name] = None
        save_sessions(sessions)


def delete_session(name: str) -> None:
    """Remove a session entirely."""
    sessions = load_sessions()
    if name in sessions:
        del sessions[name]
        save_sessions(sessions)


def cmd_list() -> None:
    sessions = load_sessions()
    if not sessions:
        print("No sessions.")
        return

    print("Sessions:")
    for name, sid in sessions.items():
        label = "(main) " if name == MAIN_SESSION else ""
        status = sid[:16] + "..." if sid else "(new)"
        print("  %-20s %s%s" % (name, label, status))


def cmd_reset(name: str) -> None:
    sessions = load_sessions()
    if name not in sessions:
        print("Session '%s' not found." % name, file=sys.stderr)
        sys.exit(1)
    reset_session(name)
    print("Session '%s' reset. Will start fresh on next use." % name)


def usage() -> None:
    print("""Usage: rex session <command>

Commands:
  list                 List all sessions
  reset <name>         Reset a session (starts fresh on next use)""")


def main() -> None:
    args = sys.argv[1:]
    cmd = args[0] if args else "list"

    if cmd == "list":
        cmd_list()
    elif cmd == "reset":
        if len(args) < 2:
            print("Usage: rex session reset <name>")
            sys.exit(1)
        cmd_reset(args[1])
    elif cmd in ("help", "-h", "--help"):
        usage()
    else:
        print("Unknown session command: %s" % cmd)
        usage()
        sys.exit(1)


if __name__ == "__main__":
    main()
