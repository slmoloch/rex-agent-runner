#!/usr/bin/env python3
"""rex send - send a prompt to a session via the bot."""
from __future__ import annotations

import json
import os
import sys
import urllib.request
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR

with open(BASE_DIR / "config.json") as f:
    CONFIG = json.load(f)

JOB_PORT = CONFIG.get("job_port", 9821)


def send(session: str, message: str) -> None:
    if session not in ("main", "new"):
        print("Error: session must be 'main' or 'new'.", file=sys.stderr)
        sys.exit(1)

    data = json.dumps({
        "prompt": message,
        "job_name": "send:%s" % session,
        "session": session,
    }).encode()
    req = urllib.request.Request(
        "http://127.0.0.1:%d/job" % JOB_PORT,
        data=data,
        headers={"Content-Type": "application/json"},
    )

    try:
        with urllib.request.urlopen(req, timeout=620) as resp:
            result = json.loads(resp.read())
            print("Sent to session '%s': %s" % (session, result.get("status", "unknown")))
    except urllib.error.URLError as e:
        print("Error: Could not connect to bot. Is it running? (%s)" % e, file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    if len(sys.argv) < 3:
        print("Usage: rex send <session> <message>")
        print()
        print("Send a prompt to a session via the bot.")
        print()
        print("Sessions:")
        print("  main             The user-facing Telegram session")
        print("  new              Ephemeral session (discarded after)")
        sys.exit(1)

    session = sys.argv[1]
    message = " ".join(sys.argv[2:])
    send(session, message)
