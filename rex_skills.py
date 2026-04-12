#!/usr/bin/env python3
"""rex skills - discover and list workspace skills."""
from __future__ import annotations

import json
import os
import re
import sys
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
CONFIG_PATH = BASE_DIR / "config.json"

with open(CONFIG_PATH) as f:
    CONFIG = json.load(f)

WORKDIR = Path(CONFIG.get("workspace", "./workspace")).resolve()
SKILLS_DIR = WORKDIR / "skills"


def _extract_description(content: str) -> str:
    """Extract description from a skill markdown file.

    Uses the first non-heading, non-empty paragraph line as description.
    Falls back to the first heading text if no paragraph found.
    """
    heading = ""
    for line in content.splitlines():
        stripped = line.strip()
        if not stripped:
            continue
        if stripped.startswith("#"):
            if not heading:
                heading = re.sub(r"^#+\s*", "", stripped)
            continue
        # First non-empty, non-heading line is the description
        return stripped
    return heading or "(no description)"


def list_skills() -> list[dict]:
    """Return a list of skill dicts with name, path, and description."""
    if not SKILLS_DIR.is_dir():
        return []

    skills = []
    for path in sorted(SKILLS_DIR.glob("*.md")):
        content = path.read_text()
        skills.append({
            "name": path.stem,
            "path": str(path),
            "description": _extract_description(content),
        })
    return skills


def load_skills_content() -> str:
    """Load all skill files and return combined content for the system prompt."""
    if not SKILLS_DIR.is_dir():
        return ""

    parts = []
    for path in sorted(SKILLS_DIR.glob("*.md")):
        content = path.read_text().strip()
        if content:
            parts.append(content)

    if not parts:
        return ""

    return "## Workspace Skills\n\n" + "\n\n---\n\n".join(parts)


def cmd_list() -> None:
    """List all skills with descriptions."""
    skills = list_skills()
    if not skills:
        print("No skills found in %s/" % SKILLS_DIR)
        return

    print("Skills:")
    for s in skills:
        print("  %-20s %s" % (s["name"], s["description"]))


def usage() -> None:
    print("""Usage: rex skills [command]

Commands:
  list           List all workspace skills (default)""")


def main() -> None:
    args = sys.argv[1:]
    cmd = args[0] if args else "list"

    if cmd in ("list", ""):
        cmd_list()
    elif cmd in ("help", "-h", "--help"):
        usage()
    else:
        print("Unknown skills command: %s" % cmd)
        usage()
        sys.exit(1)


if __name__ == "__main__":
    main()
