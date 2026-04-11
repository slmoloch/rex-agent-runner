#!/usr/bin/env python3
"""CLI tool for Claude agent to notify users and manage scheduled jobs."""

import json
import ssl
import subprocess
import sys
import urllib.request
import urllib.parse
from pathlib import Path

try:
    import certifi
    SSL_CONTEXT = ssl.create_default_context(cafile=certifi.where())
except ImportError:
    SSL_CONTEXT = None

import os as _os

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(_os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in _os.environ else INSTALL_DIR
CONFIG_PATH = BASE_DIR / "config.json"

with open(CONFIG_PATH) as f:
    CONFIG = json.load(f)

TELEGRAM_TOKEN = CONFIG["telegram_bot_token"]
CHAT_ID = CONFIG.get("telegram_chat_id", CONFIG["allowed_user_ids"][0])
WORKDIR = str((BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve())
JOBS_DIR = Path(WORKDIR) / "jobs"
VENV_PYTHON = str(INSTALL_DIR / "venv" / "bin" / "python")
JOB_SCRIPT = str(INSTALL_DIR / "rex_job.py")


def telegram_send(message: str) -> None:
    """Send a message to the user via Telegram."""
    url = f"https://api.telegram.org/bot{TELEGRAM_TOKEN}/sendMessage"
    data = urllib.parse.urlencode({
        "chat_id": CHAT_ID,
        "text": message,
        "parse_mode": "Markdown",
    }).encode()
    try:
        req = urllib.request.Request(url, data=data)
        with urllib.request.urlopen(req, context=SSL_CONTEXT) as resp:
            result = json.loads(resp.read())
            if result.get("ok"):
                print(f"Message sent to chat {CHAT_ID}")
            else:
                print(f"Telegram error: {result}", file=sys.stderr)
                sys.exit(1)
    except Exception as e:
        print(f"Failed to send message: {e}", file=sys.stderr)
        sys.exit(1)


def cron_list() -> None:
    """List all scheduled jobs from crontab."""
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
        # Extract job name from the command
        if "rex_job.py" in line:
            idx = line.index("rex_job.py") + len("rex_job.py")
            job_name = line[idx:].strip()
        else:
            job_name = "unknown"
        print(f"  {job_name}: {schedule}")


def cron_create(schedule: str, job_name: str) -> None:
    """Add a cron job for a job file."""
    job_file = JOBS_DIR / f"{job_name}.md"
    if not job_file.exists():
        print(f"Error: Job file not found: {job_file}", file=sys.stderr)
        print(f"Create {job_file} first.", file=sys.stderr)
        sys.exit(1)

    cron_line = f"{schedule} cd {BASE_DIR} && {VENV_PYTHON} {JOB_SCRIPT} {job_name} >> {BASE_DIR}/logs/{job_name}.log 2>&1"

    # Get existing crontab
    result = subprocess.run(["crontab", "-l"], capture_output=True, text=True)
    existing = result.stdout if result.returncode == 0 else ""

    # Check if job already scheduled
    if f"rex_job.py {job_name}" in existing:
        print(f"Job '{job_name}' is already scheduled. Remove it first with: agent-tool cron remove {job_name}")
        return

    # Create logs dir
    (BASE_DIR / "logs").mkdir(exist_ok=True)

    # Add new cron line
    new_crontab = existing.rstrip("\n") + "\n" + cron_line + "\n"
    proc = subprocess.run(["crontab", "-"], input=new_crontab, text=True, capture_output=True)
    if proc.returncode != 0:
        print(f"Failed to update crontab: {proc.stderr}", file=sys.stderr)
        sys.exit(1)

    print(f"Scheduled job '{job_name}' with schedule: {schedule}")


def cron_remove(job_name: str) -> None:
    """Remove a scheduled job from crontab."""
    result = subprocess.run(["crontab", "-l"], capture_output=True, text=True)
    if result.returncode != 0:
        print("No crontab configured.")
        return

    lines = result.stdout.splitlines()
    filtered = [l for l in lines if f"rex_job.py {job_name}" not in l]

    if len(filtered) == len(lines):
        print(f"Job '{job_name}' not found in crontab.")
        return

    new_crontab = "\n".join(filtered) + "\n"
    proc = subprocess.run(["crontab", "-"], input=new_crontab, text=True, capture_output=True)
    if proc.returncode != 0:
        print(f"Failed to update crontab: {proc.stderr}", file=sys.stderr)
        sys.exit(1)

    print(f"Removed job '{job_name}' from schedule.")


def jobs_list() -> None:
    """List available job files."""
    if not JOBS_DIR.exists():
        print("No jobs directory found.")
        return

    jobs = sorted(JOBS_DIR.glob("*.md"))
    if not jobs:
        print("No job files found.")
        return

    print("Available jobs:")
    for j in jobs:
        name = j.stem
        first_line = j.read_text().strip().split("\n")[0][:80]
        print(f"  {name}: {first_line}")


def usage():
    print("""Usage: python agent_tool.py <command> [args]

Commands:
  notify <message>                    Send a message to the user via Telegram
  jobs list                           List available job files in workspace/jobs/
  cron list                           List all scheduled cron jobs
  cron create "<schedule>" <job>      Schedule a job (5-field cron expression)
  cron remove <job>                   Remove a scheduled job

Examples:
  python agent_tool.py notify "Build completed successfully"
  python agent_tool.py jobs list
  python agent_tool.py cron list
  python agent_tool.py cron create "0 9 * * *" JOB1
  python agent_tool.py cron remove JOB1""")


def main():
    if len(sys.argv) < 2:
        usage()
        sys.exit(1)

    cmd = sys.argv[1]

    if cmd == "notify":
        if len(sys.argv) < 3:
            print("Error: notify requires a message", file=sys.stderr)
            sys.exit(1)
        telegram_send(" ".join(sys.argv[2:]))

    elif cmd == "jobs":
        if len(sys.argv) >= 3 and sys.argv[2] == "list":
            jobs_list()
        else:
            print("Usage: agent_tool.py jobs list")
            sys.exit(1)

    elif cmd == "cron":
        if len(sys.argv) < 3:
            print("Usage: agent_tool.py cron <list|create|remove>")
            sys.exit(1)
        subcmd = sys.argv[2]
        if subcmd == "list":
            cron_list()
        elif subcmd == "create":
            if len(sys.argv) < 5:
                print('Usage: agent_tool.py cron create "<schedule>" <job_name>')
                sys.exit(1)
            cron_create(sys.argv[3], sys.argv[4])
        elif subcmd == "remove":
            if len(sys.argv) < 4:
                print("Usage: agent_tool.py cron remove <job_name>")
                sys.exit(1)
            cron_remove(sys.argv[3])
        else:
            print(f"Unknown cron subcommand: {subcmd}")
            sys.exit(1)

    else:
        print(f"Unknown command: {cmd}")
        usage()
        sys.exit(1)


if __name__ == "__main__":
    main()
