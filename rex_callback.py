#!/usr/bin/env python3
"""Manage and execute callbacks — prompts that can be scheduled or run on demand."""
from __future__ import annotations

import json
import os
import plistlib
import shutil
import subprocess
import sys
import urllib.request
import uuid
from datetime import datetime, timedelta
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
PLIST_DIR = Path.home() / "Library" / "LaunchAgents"
PLIST_PREFIX = "com.rex.callback."

with open(BASE_DIR / "config.json") as f:
    CONFIG = json.load(f)

WORKDIR = str((BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve())
CALLBACKS_FILE = Path(WORKDIR) / "callbacks.json"
LOG_DIR = BASE_DIR / "logs"
JOB_PORT = CONFIG.get("job_port", 9821)

REX_BIN = shutil.which("rex") or str(INSTALL_DIR / "bin" / "rex")


def _load_callbacks():
    if CALLBACKS_FILE.exists():
        with open(CALLBACKS_FILE) as f:
            return json.load(f)
    return {}


def _save_callbacks(callbacks):
    with open(CALLBACKS_FILE, "w") as f:
        json.dump(callbacks, f, indent=2)
        f.write("\n")


def _plist_path(callback_id):
    return PLIST_DIR / ("%s%s.plist" % (PLIST_PREFIX, callback_id))


def _parse_cron(expr):
    fields = expr.strip().split()
    if len(fields) != 5:
        print("Error: cron expression must have 5 fields (minute hour day month weekday)", file=sys.stderr)
        sys.exit(1)

    minute, hour, day, month, weekday = fields
    keys = [
        ("Minute", minute),
        ("Hour", hour),
        ("Day", day),
        ("Month", month),
        ("Weekday", weekday),
    ]

    if "-" in weekday and weekday != "*":
        start, end = weekday.split("-", 1)
        try:
            start, end = int(start), int(end)
        except ValueError:
            print("Error: invalid weekday range: %s" % weekday, file=sys.stderr)
            sys.exit(1)
        intervals = []
        for wd in range(start, end + 1):
            interval = {}
            for key, val in keys:
                if key == "Weekday":
                    interval[key] = wd
                elif val != "*":
                    interval[key] = int(val)
            intervals.append(interval)
        return intervals

    interval = {}
    for key, val in keys:
        if val != "*":
            try:
                interval[key] = int(val)
            except ValueError:
                print("Error: unsupported cron value '%s' for %s" % (val, key), file=sys.stderr)
                sys.exit(1)

    return [interval]


def cmd_list():
    callbacks = _load_callbacks()
    if not callbacks:
        print("No callbacks registered.")
        return

    print("Callbacks:")
    for cid, cb in callbacks.items():
        if cb.get("recurring"):
            timing = "recurring: %s" % cb.get("schedule", "?")
        else:
            timing = "once: %s" % cb.get("at", "?")
        prompt_preview = cb.get("prompt", "")[:60]
        print("  %s: [%s] %s" % (cid, timing, prompt_preview))


def _parse_at(at_str):
    """Parse --at value into a datetime. Supports:
    - "HH:MM" (today, or tomorrow if already passed)
    - "YYYY-MM-DD HH:MM"
    - "+Nm" (N minutes from now)
    - "+Nh" (N hours from now)
    """
    now = datetime.now()

    if at_str.startswith("+"):
        val = at_str[1:]
        if val.endswith("m"):
            return now + timedelta(minutes=int(val[:-1]))
        elif val.endswith("h"):
            return now + timedelta(hours=int(val[:-1]))
        else:
            print("Error: use +Nm or +Nh (e.g. +5m, +2h)", file=sys.stderr)
            sys.exit(1)

    if " " in at_str:
        try:
            return datetime.strptime(at_str, "%Y-%m-%d %H:%M")
        except ValueError:
            print("Error: use format 'YYYY-MM-DD HH:MM'", file=sys.stderr)
            sys.exit(1)

    try:
        t = datetime.strptime(at_str, "%H:%M")
        dt = now.replace(hour=t.hour, minute=t.minute, second=0, microsecond=0)
        if dt <= now:
            dt += timedelta(days=1)
        return dt
    except ValueError:
        print("Error: use format 'HH:MM'", file=sys.stderr)
        sys.exit(1)


def cmd_create(prompt, schedule=None, at=None, name=None):
    callbacks = _load_callbacks()

    callback_id = name or str(uuid.uuid4())[:8]
    if callback_id in callbacks:
        print("Callback '%s' already exists." % callback_id, file=sys.stderr)
        sys.exit(1)

    if at:
        dt = _parse_at(at)
        callbacks[callback_id] = {
            "prompt": prompt,
            "recurring": False,
            "at": dt.strftime("%Y-%m-%d %H:%M"),
        }
        _save_callbacks(callbacks)
        _schedule_at(callback_id, dt)
        print("Created one-time callback: %s (at %s)" % (callback_id, dt.strftime("%Y-%m-%d %H:%M")))
    elif schedule:
        callbacks[callback_id] = {
            "prompt": prompt,
            "recurring": True,
            "schedule": schedule,
        }
        _save_callbacks(callbacks)
        _schedule_callback(callback_id, schedule)
        print("Created recurring callback: %s" % callback_id)
    else:
        print("Error: --schedule or --at is required.", file=sys.stderr)
        sys.exit(1)


def cmd_execute(callback_id):
    callbacks = _load_callbacks()

    if callback_id not in callbacks:
        print("Callback '%s' not found." % callback_id, file=sys.stderr)
        sys.exit(1)

    cb = callbacks[callback_id]
    prompt = cb["prompt"]

    # Submit to bot via HTTP
    data = json.dumps({"prompt": prompt, "job_name": callback_id}).encode()
    req = urllib.request.Request(
        "http://127.0.0.1:%d/job" % JOB_PORT,
        data=data,
        headers={"Content-Type": "application/json"},
    )

    try:
        with urllib.request.urlopen(req, timeout=620) as resp:
            result = json.loads(resp.read())
            print("Callback %s: %s" % (callback_id, result.get("status", "unknown")))
    except urllib.error.URLError as e:
        print("Error: Could not connect to bot. Is it running? (%s)" % e, file=sys.stderr)
        sys.exit(1)

    # Remove if one-time
    if not cb.get("recurring", False):
        del callbacks[callback_id]
        _save_callbacks(callbacks)
        _unschedule_callback(callback_id)
        print("One-time callback '%s' removed." % callback_id)


def cmd_remove(callback_id):
    callbacks = _load_callbacks()

    if callback_id not in callbacks:
        print("Callback '%s' not found." % callback_id, file=sys.stderr)
        sys.exit(1)

    del callbacks[callback_id]
    _save_callbacks(callbacks)
    _unschedule_callback(callback_id)
    print("Removed callback: %s" % callback_id)


def _schedule_callback(callback_id, schedule):
    plist_path = _plist_path(callback_id)
    LOG_DIR.mkdir(exist_ok=True)
    PLIST_DIR.mkdir(parents=True, exist_ok=True)

    intervals = _parse_cron(schedule)
    calendar_interval = intervals[0] if len(intervals) == 1 else intervals

    plist = {
        "Label": "%s%s" % (PLIST_PREFIX, callback_id),
        "ProgramArguments": [REX_BIN, "callback", "execute", callback_id],
        "EnvironmentVariables": {
            "REX_PROJECT_DIR": str(BASE_DIR),
            "PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"),
        },
        "StartCalendarInterval": calendar_interval,
        "StandardOutPath": str(LOG_DIR / ("callback_%s.log" % callback_id)),
        "StandardErrorPath": str(LOG_DIR / ("callback_%s.err.log" % callback_id)),
    }

    with open(plist_path, "wb") as f:
        plistlib.dump(plist, f)

    result = subprocess.run(
        ["launchctl", "load", str(plist_path)],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        print("Warning: failed to load plist: %s" % result.stderr, file=sys.stderr)


def _schedule_at(callback_id, dt):
    """Schedule a one-time callback using StartCalendarInterval with exact date."""
    plist_path = _plist_path(callback_id)
    LOG_DIR.mkdir(exist_ok=True)
    PLIST_DIR.mkdir(parents=True, exist_ok=True)

    calendar_interval = {
        "Month": dt.month,
        "Day": dt.day,
        "Hour": dt.hour,
        "Minute": dt.minute,
    }

    plist = {
        "Label": "%s%s" % (PLIST_PREFIX, callback_id),
        "ProgramArguments": [REX_BIN, "callback", "execute", callback_id],
        "EnvironmentVariables": {
            "REX_PROJECT_DIR": str(BASE_DIR),
            "PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"),
        },
        "StartCalendarInterval": calendar_interval,
        "StandardOutPath": str(LOG_DIR / ("callback_%s.log" % callback_id)),
        "StandardErrorPath": str(LOG_DIR / ("callback_%s.err.log" % callback_id)),
    }

    with open(plist_path, "wb") as f:
        plistlib.dump(plist, f)

    result = subprocess.run(
        ["launchctl", "load", str(plist_path)],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        print("Warning: failed to load plist: %s" % result.stderr, file=sys.stderr)


def _unschedule_callback(callback_id):
    plist_path = _plist_path(callback_id)
    if not plist_path.exists():
        return

    subprocess.run(
        ["launchctl", "unload", str(plist_path)],
        capture_output=True, text=True,
    )
    plist_path.unlink()


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("""Usage: rex callback <command> [args]

Commands:
  list                                          List all callbacks
  create "<prompt>" --schedule "<cron>" [--name <id>]
                                                Create a recurring callback
  create "<prompt>" --at "<time>" [--name <id>]
                                                Create a one-time callback
  execute <callback_id>                         Execute a callback
  remove <callback_id>                          Remove a callback

--at formats: "HH:MM", "YYYY-MM-DD HH:MM", "+5m", "+2h" """)
        sys.exit(1)

    cmd = sys.argv[1]

    if cmd == "list":
        cmd_list()

    elif cmd == "create":
        if len(sys.argv) < 3:
            print('Usage: rex callback create "<prompt>" --schedule "<cron>" | --at "<time>" [--name <id>]')
            sys.exit(1)
        prompt = sys.argv[2]
        schedule = None
        at = None
        name = None
        i = 3
        while i < len(sys.argv):
            if sys.argv[i] == "--schedule" and i + 1 < len(sys.argv):
                schedule = sys.argv[i + 1]
                i += 2
            elif sys.argv[i] == "--at" and i + 1 < len(sys.argv):
                at = sys.argv[i + 1]
                i += 2
            elif sys.argv[i] == "--name" and i + 1 < len(sys.argv):
                name = sys.argv[i + 1]
                i += 2
            else:
                i += 1
        if not schedule and not at:
            print("Error: --schedule or --at is required.", file=sys.stderr)
            sys.exit(1)
        if schedule and at:
            print("Error: use --schedule or --at, not both.", file=sys.stderr)
            sys.exit(1)
        cmd_create(prompt, schedule=schedule, at=at, name=name)

    elif cmd == "execute":
        if len(sys.argv) < 3:
            print("Usage: rex callback execute <callback_id>")
            sys.exit(1)
        cmd_execute(sys.argv[2])

    elif cmd == "remove":
        if len(sys.argv) < 3:
            print("Usage: rex callback remove <callback_id>")
            sys.exit(1)
        cmd_remove(sys.argv[2])

    else:
        print("Unknown callback command: %s" % cmd)
        sys.exit(1)
