#!/usr/bin/env python3
"""rex db - SQLite event store, populated from the JSONL event stream."""
from __future__ import annotations

import json
import os
import sqlite3
import sys
from datetime import datetime, timedelta
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR

with open(BASE_DIR / "config.json") as f:
    CONFIG = json.load(f)

WORKDIR = (BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve()
DB_PATH = WORKDIR / "events.db"

_SCHEMA = """
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    session TEXT,
    session_id TEXT,
    trigger TEXT,
    prompt_preview TEXT,
    response_preview TEXT,
    cost_usd REAL,
    duration_ms INTEGER,
    num_turns INTEGER,
    caller_session TEXT
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_sid ON events(session_id);
"""

_COLUMNS = (
    "timestamp", "session", "session_id", "trigger",
    "prompt_preview", "response_preview",
    "cost_usd", "duration_ms", "num_turns", "caller_session",
)


def _connect() -> sqlite3.Connection:
    WORKDIR.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(str(DB_PATH), timeout=10)
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA busy_timeout=5000")
    return conn


def init_db() -> None:
    conn = _connect()
    conn.executescript(_SCHEMA)
    conn.close()


def insert_event(event: dict) -> None:
    vals = tuple(event.get(c) for c in _COLUMNS)
    conn = _connect()
    try:
        conn.execute(
            "INSERT INTO events (%s) VALUES (%s)"
            % (", ".join(_COLUMNS), ", ".join("?" * len(_COLUMNS))),
            vals,
        )
        conn.commit()
    finally:
        conn.close()


def query_events(days: int = 7, since: str | None = None) -> list[dict]:
    conn = _connect()
    conn.row_factory = sqlite3.Row
    try:
        if since:
            rows = conn.execute(
                "SELECT * FROM events WHERE timestamp > ? ORDER BY timestamp DESC",
                (since,),
            ).fetchall()
        else:
            cutoff = (datetime.now() - timedelta(days=days)).isoformat()
            rows = conn.execute(
                "SELECT * FROM events WHERE timestamp >= ? ORDER BY timestamp DESC",
                (cutoff,),
            ).fetchall()
        return [_row_to_dict(r) for r in rows]
    finally:
        conn.close()


def _row_to_dict(row: sqlite3.Row) -> dict:
    d = {}
    for c in _COLUMNS:
        v = row[c]
        if v is not None:
            d[c] = v
    return d


def clear_db() -> None:
    conn = _connect()
    conn.execute("DELETE FROM events")
    conn.commit()
    conn.close()


def rebuild_from_logs() -> int:
    """Clear the database and repopulate from all JSONL event log files."""
    from rex_events import EVENTS_DIR, LEGACY_EVENTS_FILE

    init_db()
    conn = _connect()
    conn.execute("DELETE FROM events")

    count = 0
    insert_sql = "INSERT INTO events (%s) VALUES (%s)" % (
        ", ".join(_COLUMNS), ", ".join("?" * len(_COLUMNS)),
    )

    all_files: list[Path] = []
    if EVENTS_DIR.is_dir():
        all_files.extend(sorted(EVENTS_DIR.glob("events_*.jsonl")))
    if LEGACY_EVENTS_FILE.exists():
        all_files.append(LEGACY_EVENTS_FILE)

    for path in all_files:
        with open(path) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    continue
                vals = tuple(event.get(c) for c in _COLUMNS)
                conn.execute(insert_sql, vals)
                count += 1

    conn.commit()
    conn.close()
    return count


# --- CLI ---

def usage() -> None:
    print("""Usage: rex db <command>

Commands:
  rebuild    Clear SQLite and repopulate from JSONL event logs
  clear      Delete all events from SQLite
  stats      Show database statistics""")


def main() -> None:
    args = sys.argv[1:]

    if not args or args[0] in ("help", "-h", "--help"):
        usage()
        return

    cmd = args[0]

    if cmd == "rebuild":
        print("Rebuilding database from event logs...")
        count = rebuild_from_logs()
        print("Done. Inserted %d events into %s" % (count, DB_PATH))
    elif cmd == "clear":
        init_db()
        clear_db()
        print("Database cleared: %s" % DB_PATH)
    elif cmd == "stats":
        init_db()
        conn = _connect()
        total = conn.execute("SELECT COUNT(*) FROM events").fetchone()[0]
        oldest = conn.execute("SELECT MIN(timestamp) FROM events").fetchone()[0]
        newest = conn.execute("SELECT MAX(timestamp) FROM events").fetchone()[0]
        cost = conn.execute("SELECT SUM(cost_usd) FROM events").fetchone()[0] or 0
        sessions = conn.execute("SELECT COUNT(DISTINCT session_id) FROM events").fetchone()[0]
        conn.close()
        print("Database: %s" % DB_PATH)
        print("Events:   %d" % total)
        print("Sessions: %d" % sessions)
        print("Range:    %s to %s" % (oldest or "-", newest or "-"))
        print("Cost:     $%.4f" % cost)
    else:
        print("Unknown db command: %s" % cmd)
        usage()
        sys.exit(1)


if __name__ == "__main__":
    main()
