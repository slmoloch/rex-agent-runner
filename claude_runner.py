"""Shared Claude Code CLI runner."""

from __future__ import annotations

import json
import logging
import os
import shutil
import subprocess
from pathlib import Path

logger = logging.getLogger(__name__)

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
CONFIG_PATH = BASE_DIR / "config.json"

with open(CONFIG_PATH) as f:
    CONFIG = json.load(f)

WORKDIR = str(Path(CONFIG.get("workspace", "./workspace")).resolve())

def _find_claude() -> str:
    if CONFIG.get("claude_bin"):
        return CONFIG["claude_bin"]
    found = shutil.which("claude")
    if found:
        return found
    for path in [
        Path.home() / ".local" / "bin" / "claude",
        Path.home() / ".claude" / "local" / "claude",
        Path("/usr/local/bin/claude"),
        Path("/opt/homebrew/bin/claude"),
    ]:
        if path.exists():
            return str(path)
    return "claude"

CLAUDE_BIN = _find_claude()

REX_PROMPT_PATH = INSTALL_DIR / "rex_system_prompt.md"
AGENT_PROMPT_PATH = Path(WORKDIR) / "AGENT.md"
SKILLS_DIR = Path(WORKDIR) / "skills"


def _load_skills_content() -> str:
    """Load all .md files from the workspace skills/ directory."""
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


_parts = []
_skills = _load_skills_content()
if _skills:
    _parts.append(_skills)
if REX_PROMPT_PATH.exists():
    _parts.append(REX_PROMPT_PATH.read_text().strip())
if AGENT_PROMPT_PATH.exists():
    _parts.append(AGENT_PROMPT_PATH.read_text().strip())
SYSTEM_PROMPT = "\n\n".join(_parts)


def run_claude(
    prompt: str,
    session_id: str | None = None,
    system_prompt: str | None = None,
    timeout: int = 300,
) -> dict:
    """Run claude CLI and return a result dict.

    Returns: {"response": str, "session_id": str|None,
              "cost_usd": float, "duration_ms": int, "num_turns": int}
    """
    cmd = [
        CLAUDE_BIN, "-p", prompt,
        "--output-format", "stream-json", "--verbose",
        "--dangerously-skip-permissions",
        "--disallowedTools", "CronCreate,CronDelete,CronList,TaskCreate,TaskGet,TaskList,TaskUpdate,TodoWrite",
    ]

    if session_id:
        cmd.extend(["-r", session_id])
    elif system_prompt or SYSTEM_PROMPT:
        cmd.extend(["--system-prompt", system_prompt or SYSTEM_PROMPT])

    logger.info("Prompt: %s", prompt[:200])

    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        cwd=WORKDIR,
        timeout=timeout,
    )

    if result.returncode != 0:
        stderr = result.stderr.strip()
        stdout = result.stdout.strip()
        logger.error("claude exited %d: stderr=%s stdout=%s", result.returncode, stderr, stdout[:500])
        error_msg = stderr or stdout or "claude exited with code %d" % result.returncode
        return {
            "response": "Error: %s" % error_msg,
            "session_id": session_id,
            "cost_usd": 0, "duration_ms": 0, "num_turns": 0,
        }

    response_text = ""
    new_session_id = session_id
    cost = 0
    turns = 0
    duration = 0

    for line in result.stdout.splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue

        etype = event.get("type")

        if etype == "result":
            response_text = event.get("result", "")
            new_session_id = event.get("session_id", session_id)
            cost = event.get("total_cost_usd", 0)
            turns = event.get("num_turns", 0)
            duration = event.get("duration_ms", 0)
            logger.info("Result: turns=%d, duration=%dms, cost=$%.4f", turns, duration, cost)

        elif etype == "assistant":
            message = event.get("message", {})
            for block in message.get("content", []):
                if block.get("type") == "tool_use":
                    logger.info("Tool: %s(%s)", block.get("name"), json.dumps(block.get("input", {}))[:200])
                elif block.get("type") == "text":
                    text = block.get("text", "")
                    if text:
                        logger.info("Claude: %s", text[:300])

    return {
        "response": response_text,
        "session_id": new_session_id,
        "cost_usd": cost,
        "duration_ms": duration,
        "num_turns": turns,
    }
