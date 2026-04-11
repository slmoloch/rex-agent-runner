#!/usr/bin/env python3
"""Submit a job to the bot process via HTTP."""

import json
import sys
import urllib.request
from pathlib import Path

BASE_DIR = Path(__file__).parent
CONFIG_PATH = BASE_DIR / "config.json"

with open(CONFIG_PATH) as f:
    CONFIG = json.load(f)

WORKDIR = str((BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve())
JOBS_DIR = Path(WORKDIR) / "jobs"
JOB_PORT = CONFIG.get("job_port", 9821)


def run_job(job_name: str) -> None:
    job_file = JOBS_DIR / f"{job_name}.md"
    if not job_file.exists():
        print(f"Error: Job file not found: {job_file}", file=sys.stderr)
        sys.exit(1)

    prompt = job_file.read_text().strip()
    data = json.dumps({"prompt": prompt, "job_name": job_name}).encode()

    req = urllib.request.Request(
        f"http://127.0.0.1:{JOB_PORT}/job",
        data=data,
        headers={"Content-Type": "application/json"},
    )

    try:
        with urllib.request.urlopen(req, timeout=620) as resp:
            result = json.loads(resp.read())
            print(f"Job {job_name}: {result.get('status', 'unknown')}")
    except urllib.error.URLError as e:
        print(f"Error: Could not connect to bot. Is it running? ({e})", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print(f"Usage: {sys.argv[0]} <job_name>")
        print(f"  Jobs dir: {JOBS_DIR}")
        sys.exit(1)
    run_job(sys.argv[1])
