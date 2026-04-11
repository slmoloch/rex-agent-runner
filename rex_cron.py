#!/usr/bin/env python3
"""Manage launchd schedules for rex jobs."""
from __future__ import annotations

import json
import os
import plistlib
import shutil
import subprocess
import sys
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
PLIST_DIR = Path.home() / "Library" / "LaunchAgents"
PLIST_PREFIX = "com.rex.job."

with open(BASE_DIR / "config.json") as f:
    CONFIG = json.load(f)

WORKDIR = str((BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve())
JOBS_DIR = Path(WORKDIR) / "jobs"
LOG_DIR = BASE_DIR / "logs"

REX_BIN = shutil.which("rex") or str(INSTALL_DIR / "bin" / "rex")


def _plist_path(job_name):
    return PLIST_DIR / ("%s%s.plist" % (PLIST_PREFIX, job_name))


def _parse_cron(expr):
    """Parse 5-field cron expression into StartCalendarInterval dict(s).
    Supports: exact values, wildcards (*), weekday ranges (1-5).
    Returns a list of dicts (one per interval needed)."""
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

    # Check for weekday range (e.g. 1-5)
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
                print("Error: unsupported cron value '%s' for %s. Use exact numbers or *." % (val, key), file=sys.stderr)
                sys.exit(1)

    return [interval]


def cron_list():
    PLIST_DIR.mkdir(parents=True, exist_ok=True)
    plists = sorted(PLIST_DIR.glob("%s*.plist" % PLIST_PREFIX))

    if not plists:
        print("No scheduled jobs found.")
        return

    print("Scheduled jobs:")
    for p in plists:
        job_name = p.stem.replace(PLIST_PREFIX, "")
        with open(p, "rb") as f:
            plist = plistlib.load(f)

        intervals = plist.get("StartCalendarInterval", {})
        if isinstance(intervals, dict):
            intervals = [intervals]

        # Check if loaded
        result = subprocess.run(
            ["launchctl", "list", "%s%s" % (PLIST_PREFIX, job_name)],
            capture_output=True, text=True,
        )
        status = "active" if result.returncode == 0 else "inactive"

        for interval in intervals:
            parts = []
            for key in ["Minute", "Hour", "Day", "Month", "Weekday"]:
                parts.append(str(interval.get(key, "*")))
            schedule = " ".join(parts)
            print("  %s: %s (%s)" % (job_name, schedule, status))


def cron_create(schedule, job_name):
    job_file = JOBS_DIR / ("%s.md" % job_name)
    if not job_file.exists():
        print("Error: Job file not found: %s" % job_file, file=sys.stderr)
        sys.exit(1)

    plist_path = _plist_path(job_name)
    if plist_path.exists():
        print("Job '%s' is already scheduled. Remove it first with: rex cron remove %s" % (job_name, job_name))
        return

    LOG_DIR.mkdir(exist_ok=True)
    PLIST_DIR.mkdir(parents=True, exist_ok=True)

    intervals = _parse_cron(schedule)
    calendar_interval = intervals[0] if len(intervals) == 1 else intervals

    plist = {
        "Label": "%s%s" % (PLIST_PREFIX, job_name),
        "ProgramArguments": [REX_BIN, "job", job_name],
        "EnvironmentVariables": {
            "REX_PROJECT_DIR": str(BASE_DIR),
            "PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"),
        },
        "StartCalendarInterval": calendar_interval,
        "StandardOutPath": str(LOG_DIR / ("%s.log" % job_name)),
        "StandardErrorPath": str(LOG_DIR / ("%s.err.log" % job_name)),
    }

    with open(plist_path, "wb") as f:
        plistlib.dump(plist, f)

    result = subprocess.run(
        ["launchctl", "load", str(plist_path)],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        print("Warning: failed to load plist: %s" % result.stderr, file=sys.stderr)

    print("Scheduled job '%s' with schedule: %s" % (job_name, schedule))


def cron_remove(job_name):
    plist_path = _plist_path(job_name)
    if not plist_path.exists():
        print("Job '%s' not found." % job_name)
        return

    subprocess.run(
        ["launchctl", "unload", str(plist_path)],
        capture_output=True, text=True,
    )
    plist_path.unlink()
    print("Removed job '%s' from schedule." % job_name)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: rex cron <list|create|remove>")
        sys.exit(1)

    cmd = sys.argv[1]
    if cmd == "list":
        cron_list()
    elif cmd == "create":
        if len(sys.argv) < 4:
            print('Usage: rex cron create "<schedule>" <job_name>')
            sys.exit(1)
        cron_create(sys.argv[2], sys.argv[3])
    elif cmd == "remove":
        if len(sys.argv) < 3:
            print("Usage: rex cron remove <job_name>")
            sys.exit(1)
        cron_remove(sys.argv[2])
    else:
        print("Unknown cron command: %s" % cmd)
        sys.exit(1)
