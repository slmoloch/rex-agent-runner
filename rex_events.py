#!/usr/bin/env python3
"""rex events - append-only event log with hourly file rotation."""
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
EVENTS_DIR = WORKDIR / "events"
LEGACY_EVENTS_FILE = WORKDIR / "events.jsonl"


def _current_events_path() -> Path:
    """Return the events file path for the current hour."""
    EVENTS_DIR.mkdir(exist_ok=True)
    return EVENTS_DIR / datetime.now().strftime("events_%Y-%m-%d_%H.jsonl")


def append_event(event: dict) -> None:
    """Append an event to the current hour's log file and to SQLite."""
    if "timestamp" not in event:
        event["timestamp"] = datetime.now().isoformat()
    with open(_current_events_path(), "a") as f:
        f.write(json.dumps(event) + "\n")
    try:
        from rex_db import insert_event as db_insert, init_db
        init_db()
        db_insert(event)
    except Exception:
        pass


def _read_events_from(path: Path, cutoff: datetime) -> list[dict]:
    """Read events from a single JSONL file, filtering by cutoff."""
    events = []
    if not path.exists():
        return events
    with open(path) as f:
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
    return events


def load_events(days: int = 7) -> list[dict]:
    """Load events from the last N days, newest first.

    Reads from hourly event files in events/ and falls back to the
    legacy events.jsonl for events written before the migration.
    """
    cutoff = datetime.now() - timedelta(days=days)
    cutoff_hour = cutoff.replace(minute=0, second=0, microsecond=0)
    events: list[dict] = []

    # Read from hourly-bucketed files
    if EVENTS_DIR.is_dir():
        for path in sorted(EVENTS_DIR.glob("events_*.jsonl")):
            # Quick skip: parse the hour from the filename
            try:
                stem = path.stem  # events_2026-04-14_13
                dt_str = stem[len("events_"):]  # 2026-04-14_13
                file_hour = datetime.strptime(dt_str, "%Y-%m-%d_%H")
                if file_hour < cutoff_hour:
                    continue
            except ValueError:
                pass  # Can't parse — read it anyway
            events.extend(_read_events_from(path, cutoff))

    # Also read the legacy single-file log (pre-migration events)
    if LEGACY_EVENTS_FILE.exists():
        events.extend(_read_events_from(LEGACY_EVENTS_FILE, cutoff))

    events.sort(key=lambda e: e.get("timestamp", ""), reverse=True)
    return events
