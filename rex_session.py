#!/usr/bin/env python3
"""rex session - manage the main Claude session."""
from __future__ import annotations

import json
import os
import sys
from datetime import datetime
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR

with open(BASE_DIR / "config.json") as f:
    CONFIG = json.load(f)

WORKDIR = (BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve()
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
    old_session_id = data.get(MAIN_SESSION)
    data[MAIN_SESSION] = None
    _save(data)
    if old_session_id:
        from rex_events import append_event
        append_event({
            "session": MAIN_SESSION,
            "session_id": old_session_id,
            "trigger": "reset",
            "prompt_preview": "Main session reset",
            "response_preview": "",
            "cost_usd": 0,
            "duration_ms": 0,
            "num_turns": 0,
        })


def resolve_session_id(target: str) -> str | None:
    """Resolve a session target to a Claude session ID.

    target: "main" resolves to the stored main session ID.
            "new" always returns None (fresh session).
            Anything else is treated as a raw Claude session ID.
    """
    if target == "new":
        return None
    if target == MAIN_SESSION:
        return get_main_session_id()
    # Raw session ID — pass through directly
    return target


# --- Session registry ---

def register_session(session_id: str, name: str | None = None) -> None:
    """Register a session in the tracker and update its last activity."""
    if not session_id:
        return
    data = _load()
    tracked = data.setdefault("tracked", {})
    now = datetime.now().isoformat()
    if session_id not in tracked:
        tracked[session_id] = {"name": name, "last_activity": now}
    else:
        tracked[session_id]["last_activity"] = now
        if name and not tracked[session_id].get("name"):
            tracked[session_id]["name"] = name
    _save(data)


def unregister_session(session_id: str) -> None:
    """Remove a session from the tracker."""
    data = _load()
    tracked = data.get("tracked", {})
    if session_id in tracked:
        del tracked[session_id]
        _save(data)


def get_tracked_sessions() -> dict:
    """Return all tracked sessions: {session_id: {name, last_activity}}."""
    return _load().get("tracked", {})


def cmd_reset() -> None:
    from claude_runner import run_claude

    session_id = get_main_session_id()
    if session_id:
        print("Preparing session for reset…")
        prepare_prompt = "Heads up: your session is about to be reset."
        try:
            run_claude(prepare_prompt, session_id=session_id, timeout=300)
        except Exception as exc:
            print("Warning: preparation prompt failed: %s" % exc)

    reset_main_session()
    print("Main session reset. Will start fresh on next message.")


def cmd_gc() -> None:
    from rex_gc import collect
    cleaned = collect()
    if cleaned:
        print("Cleaned %d session(s): %s" % (len(cleaned), ", ".join(cleaned)))
    else:
        print("No sessions to clean.")


def cmd_list_tracked() -> None:
    tracked = get_tracked_sessions()
    main_id = get_main_session_id()
    if not tracked:
        print("No tracked sessions.")
        return
    print("Tracked sessions:")
    for sid, info in tracked.items():
        name = info.get("name") or ""
        last = info.get("last_activity", "?")
        is_main = " (main)" if sid == main_id else ""
        label = " [%s]" % name if name else ""
        print("  %s%s%s  last_activity=%s" % (sid, label, is_main, last))


def usage() -> None:
    print("""Usage: rex session <command>

Commands:
  reset          Reset the main session (starts fresh on next use)
  list           List all tracked sessions
  gc             Run session garbage collection""")


def main() -> None:
    args = sys.argv[1:]

    if not args or args[0] in ("help", "-h", "--help"):
        usage()
        return

    cmd = args[0]

    if cmd == "reset":
        cmd_reset()
    elif cmd == "list":
        cmd_list_tracked()
    elif cmd == "gc":
        cmd_gc()
    else:
        print("Unknown session command: %s" % cmd)
        usage()
        sys.exit(1)


if __name__ == "__main__":
    main()
