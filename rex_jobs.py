#!/usr/bin/env python3
"""List available job files."""
from __future__ import annotations

import os
import sys
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR

import json
with open(BASE_DIR / "config.json") as f:
    CONFIG = json.load(f)

WORKDIR = str((BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve())
JOBS_DIR = Path(WORKDIR) / "jobs"


def list_jobs():
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
        print("  %s: %s" % (name, first_line))


if __name__ == "__main__":
    if len(sys.argv) >= 2 and sys.argv[1] == "list":
        list_jobs()
    else:
        print("Usage: rex jobs list")
        sys.exit(1)
