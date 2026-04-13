#!/usr/bin/env python3
"""rex events - append-only event log for session activity."""
from __future__ import annotations

import json
import os
from datetime import datetime, timedelta
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR

with open(BASE_DIR / "config.json") as f:
    CONFIG = json.load(f)

WORKDIR = (BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve()
EVENTS_FILE = WORKDIR / "events.jsonl"


def append_event(event: dict) -> None:
    """Append an event to the log. Adds timestamp if not present."""
    if "timestamp" not in event:
        event["timestamp"] = datetime.now().isoformat()
    with open(EVENTS_FILE, "a") as f:
        f.write(json.dumps(event) + "\n")


def load_events(days: int = 7) -> list[dict]:
    """Load events from the last N days, newest first."""
    if not EVENTS_FILE.exists():
        return []

    cutoff = datetime.now() - timedelta(days=days)
    events = []

    with open(EVENTS_FILE) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            ts = event.get("timestamp", "")
            try:
                dt = datetime.fromisoformat(ts)
                if dt >= cutoff:
                    events.append(event)
            except ValueError:
                events.append(event)

    events.sort(key=lambda e: e.get("timestamp", ""), reverse=True)
    return events
