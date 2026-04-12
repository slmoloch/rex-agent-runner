#!/usr/bin/env python3
"""rex session - manage the main Claude session."""
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


def _load() -> dict:
    if SESSIONS_FILE.exists():
        with open(SESSIONS_FILE) as f:
            return json.load(f)
    return {}


def _save(data: dict) -> None:
    with open(SESSIONS_FILE, "w") as f:
        json.dump(data, f, indent=2)
        f.write("\n")


def get_main_session_id() -> str | None:
    """Get the Claude session ID for the main session."""
    return _load().get(MAIN_SESSION)


def set_main_session_id(session_id: str) -> None:
    """Store the Claude session ID for the main session."""
    data = _load()
    data[MAIN_SESSION] = session_id
    _save(data)


def reset_main_session() -> None:
    """Reset the main session so it starts fresh on next use."""
    data = _load()
    data[MAIN_SESSION] = None
    _save(data)


def resolve_session_id(target: str) -> str | None:
    """Resolve a session target to a Claude session ID.

    target: "main" resolves to the stored main session ID.
            "new" always returns None (fresh session).
    """
    if target == "new":
        return None
    if target == MAIN_SESSION:
        return get_main_session_id()
    # Unknown target treated as "new"
    return None


def cmd_reset() -> None:
    reset_main_session()
    print("Main session reset. Will start fresh on next message.")


def usage() -> None:
    print("""Usage: rex session <command>

Commands:
  reset          Reset the main session (starts fresh on next use)""")


def main() -> None:
    args = sys.argv[1:]

    if not args or args[0] in ("help", "-h", "--help"):
        usage()
        return

    cmd = args[0]

    if cmd == "reset":
        cmd_reset()
    else:
        print("Unknown session command: %s" % cmd)
        usage()
        sys.exit(1)


if __name__ == "__main__":
    main()
