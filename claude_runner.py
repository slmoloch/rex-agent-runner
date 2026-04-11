"""Shared Claude Code CLI runner."""

import json
import logging
import os
import subprocess
from pathlib import Path

logger = logging.getLogger(__name__)

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
CONFIG_PATH = BASE_DIR / "config.json"

with open(CONFIG_PATH) as f:
    CONFIG = json.load(f)

WORKDIR = str(Path(CONFIG.get("workspace", "./workspace")).resolve())

AGENT_PROMPT_PATH = Path(WORKDIR) / "AGENT.md"
SYSTEM_PROMPT = AGENT_PROMPT_PATH.read_text().strip() if AGENT_PROMPT_PATH.exists() else ""


def run_claude(
    prompt: str,
    session_id: str | None = None,
    system_prompt: str | None = None,
    timeout: int = 300,
) -> tuple[str, str | None]:
    """Run claude CLI and return (response_text, session_id)."""
    cmd = [
        "/Users/moloch/.local/bin/claude", "-p", prompt,
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
        return f"Error: {stderr or stdout or 'claude exited with code ' + str(result.returncode)}", session_id

    response_text = ""
    new_session_id = session_id

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

    return response_text, new_session_id
