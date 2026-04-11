#!/usr/bin/env python3
"""Manage cron schedules for rex jobs."""
from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR

with open(BASE_DIR / "config.json") as f:
    CONFIG = json.load(f)

WORKDIR = str((BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve())
JOBS_DIR = Path(WORKDIR) / "jobs"
VENV_PYTHON = str(INSTALL_DIR / "venv" / "bin" / "python")
JOB_SCRIPT = str(INSTALL_DIR / "rex_job.py")


def cron_list():
    result = subprocess.run(["crontab", "-l"], capture_output=True, text=True)
    if result.returncode != 0:
        print("No crontab configured.")
        return

    lines = [l for l in result.stdout.splitlines() if "rex_job.py" in l]
    if not lines:
        print("No scheduled jobs found.")
        return

    print("Scheduled jobs:")
    for line in lines:
        parts = line.split()
        schedule = " ".join(parts[:5])
        if "rex_job.py" in line:
            idx = line.index("rex_job.py") + len("rex_job.py")
            job_name = line[idx:].strip().split(" ")[0]
        else:
            job_name = "unknown"
        print("  %s: %s" % (job_name, schedule))


def cron_create(schedule, job_name):
    job_file = JOBS_DIR / ("%s.md" % job_name)
    if not job_file.exists():
        print("Error: Job file not found: %s" % job_file, file=sys.stderr)
        sys.exit(1)

    cron_line = "%s cd %s && %s %s %s >> %s/logs/%s.log 2>&1" % (
        schedule, BASE_DIR, VENV_PYTHON, JOB_SCRIPT, job_name, BASE_DIR, job_name,
    )

    result = subprocess.run(["crontab", "-l"], capture_output=True, text=True)
    existing = result.stdout if result.returncode == 0 else ""

    if "rex_job.py %s" % job_name in existing:
        print("Job '%s' is already scheduled. Remove it first with: rex cron remove %s" % (job_name, job_name))
        return

    (BASE_DIR / "logs").mkdir(exist_ok=True)

    new_crontab = existing.rstrip("\n") + "\n" + cron_line + "\n"
    proc = subprocess.run(["crontab", "-"], input=new_crontab, text=True, capture_output=True)
    if proc.returncode != 0:
        print("Failed to update crontab: %s" % proc.stderr, file=sys.stderr)
        sys.exit(1)

    print("Scheduled job '%s' with schedule: %s" % (job_name, schedule))


def cron_remove(job_name):
    result = subprocess.run(["crontab", "-l"], capture_output=True, text=True)
    if result.returncode != 0:
        print("No crontab configured.")
        return

    lines = result.stdout.splitlines()
    filtered = [l for l in lines if "rex_job.py %s" % job_name not in l]

    if len(filtered) == len(lines):
        print("Job '%s' not found in crontab." % job_name)
        return

    new_crontab = "\n".join(filtered) + "\n"
    proc = subprocess.run(["crontab", "-"], input=new_crontab, text=True, capture_output=True)
    if proc.returncode != 0:
        print("Failed to update crontab: %s" % proc.stderr, file=sys.stderr)
        sys.exit(1)

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
