"""Shared Claude Code CLI runner."""

from __future__ import annotations

import json
import logging
import os
import selectors
import shutil
import subprocess
import time
import uuid
from datetime import datetime
from pathlib import Path

logger = logging.getLogger(__name__)

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
CONFIG_PATH = BASE_DIR / "config.json"

with open(CONFIG_PATH) as f:
    CONFIG = json.load(f)

WORKDIR = str((BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve())

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
REX_DIR = Path(WORKDIR) / ".rex"
TURN_MARKER_DIR = REX_DIR / "turn-markers"


def _load_skills_content() -> str:
    """Load all .md files from the workspace skills/ directory."""
    if not SKILLS_DIR.is_dir():
        return ""
    parts = []
    for path in sorted(SKILLS_DIR.glob("*/SKILL.md")):
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


DEFAULT_IDLE_TIMEOUT = 600  # seconds of no stream output before we consider Claude stuck
DEFAULT_MAX_TIMEOUT: int | None = None  # no absolute wall-clock cap by default


def _drain_turn_markers(turn_id: str) -> list[dict]:
    """Read and delete the marker file written by `rex user` calls.

    Each successful `rex user` invocation appends one JSON line to
    <workspace>/.rex/turn-markers/<turn_id>.jsonl. We read it, delete it,
    and return the parsed list of sends so the caller can decide whether
    to suppress the fallback response and can log what was delivered.
    """
    path = TURN_MARKER_DIR / ("%s.jsonl" % turn_id)
    if not path.exists():
        return []
    sends: list[dict] = []
    try:
        with open(path) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    sends.append(json.loads(line))
                except json.JSONDecodeError:
                    continue
    except Exception:
        logger.exception("failed to read turn marker %s", path)
    try:
        path.unlink()
    except Exception:
        pass
    return sends


def _parse_event(line: str, state: dict) -> None:
    """Parse one stream-json line and update state dict in-place."""
    if not line.strip():
        return
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        return

    etype = event.get("type")

    if etype == "result":
        state["response_text"] = event.get("result", "")
        state["session_id"] = event.get("session_id", state.get("session_id"))
        state["cost"] = event.get("total_cost_usd", 0)
        state["turns"] = event.get("num_turns", 0)
        state["duration"] = event.get("duration_ms", 0)
        logger.info(
            "Result: turns=%d, duration=%dms, cost=$%.4f",
            state["turns"], state["duration"], state["cost"],
        )
    elif etype == "assistant":
        message = event.get("message", {})
        for block in message.get("content", []):
            if block.get("type") == "tool_use":
                name = block.get("name")
                input_preview = json.dumps(block.get("input", {}))[:500]
                logger.info("Tool: %s(%s)", name, input_preview[:200])
                state.setdefault("tools", []).append(
                    {"name": name, "input": input_preview}
                )
            elif block.get("type") == "text":
                text = block.get("text", "")
                if text:
                    logger.info("Claude: %s", text[:300])


def run_claude(
    prompt: str,
    session_id: str | None = None,
    system_prompt: str | None = None,
    timeout: int | None = None,
    idle_timeout: int | None = None,
) -> dict:
    """Run claude CLI and return a result dict.

    Activity is measured by lines arriving on the stream-json stdout. Each tool
    call, assistant text block, or result event resets the idle clock, so a
    long-running turn that is actively doing work won't be killed. If no output
    arrives for `idle_timeout` seconds we assume the process is stuck and kill
    it. `timeout` is an absolute wall-clock cap (None = unlimited).

    Returns: {"response": str, "session_id": str|None,
              "cost_usd": float, "duration_ms": int, "num_turns": int}
    """
    if idle_timeout is None:
        idle_timeout = DEFAULT_IDLE_TIMEOUT
    if timeout is None:
        timeout = DEFAULT_MAX_TIMEOUT

    cmd = [
        CLAUDE_BIN, "-p", prompt,
        "--output-format", "stream-json", "--verbose",
        "--dangerously-skip-permissions",
        "--disallowedTools", "CronCreate,CronDelete,CronList,TaskCreate,TaskGet,TaskList,TaskUpdate,TodoWrite",
    ]

    if session_id:
        cmd.extend(["-r", session_id])
    elif system_prompt or SYSTEM_PROMPT:
        base_prompt = system_prompt or SYSTEM_PROMPT
        date_line = "\n\n## Current Date\n\nToday is %s." % datetime.now().strftime("%Y-%m-%d")
        cmd.extend(["--system-prompt", base_prompt + date_line])

    logger.info("Prompt: %s", prompt[:200])

    turn_id = uuid.uuid4().hex
    env = os.environ.copy()
    env["REX_TURN_ID"] = turn_id
    if session_id:
        env["REX_SESSION_ID"] = session_id

    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        cwd=WORKDIR,
        env=env,
        bufsize=0,
    )

    state: dict = {
        "response_text": "",
        "session_id": session_id,
        "cost": 0,
        "turns": 0,
        "duration": 0,
        "tools": [],
    }
    stdout_buf = b""
    stderr_chunks: list[bytes] = []

    sel = selectors.DefaultSelector()
    sel.register(proc.stdout, selectors.EVENT_READ, "stdout")
    sel.register(proc.stderr, selectors.EVENT_READ, "stderr")
    open_streams = 2

    start = time.monotonic()
    last_activity = start
    timeout_reason: str | None = None

    # Poll in short slices so we can detect idleness / total-timeout while
    # still streaming as events arrive.
    poll_interval = min(5.0, max(1.0, idle_timeout / 10))

    try:
        while open_streams > 0:
            now = time.monotonic()
            idle_for = now - last_activity
            elapsed = now - start
            if idle_for > idle_timeout:
                timeout_reason = (
                    "no output for %ds (idle_timeout=%ds)" % (int(idle_for), idle_timeout)
                )
                break
            if timeout is not None and elapsed > timeout:
                timeout_reason = "exceeded max timeout of %ds" % timeout
                break

            events = sel.select(timeout=poll_interval)
            if not events:
                logger.debug("Claude still running: elapsed=%ds idle=%ds", int(elapsed), int(idle_for))
                continue

            for key, _ in events:
                chunk = key.fileobj.read1(65536) if hasattr(key.fileobj, "read1") else os.read(key.fileobj.fileno(), 65536)
                if not chunk:
                    sel.unregister(key.fileobj)
                    open_streams -= 1
                    continue
                last_activity = time.monotonic()
                if key.data == "stdout":
                    stdout_buf += chunk
                    while b"\n" in stdout_buf:
                        raw_line, stdout_buf = stdout_buf.split(b"\n", 1)
                        _parse_event(raw_line.decode("utf-8", errors="replace"), state)
                else:
                    stderr_chunks.append(chunk)
    finally:
        sel.close()

    if timeout_reason:
        logger.error("claude killed: %s", timeout_reason)
        proc.kill()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            logger.error("claude did not exit after SIGKILL")
        return {
            "response": "Error: Claude killed (%s)." % timeout_reason,
            "session_id": session_id,
            "cost_usd": 0, "duration_ms": 0, "num_turns": 0,
            "tools": state["tools"],
            "rex_user_sends": _drain_turn_markers(turn_id),
        }

    returncode = proc.wait()

    # Drain any trailing partial line
    if stdout_buf.strip():
        _parse_event(stdout_buf.decode("utf-8", errors="replace"), state)

    rex_user_sends = _drain_turn_markers(turn_id)

    if returncode != 0:
        stderr = b"".join(stderr_chunks).decode("utf-8", errors="replace").strip()
        logger.error("claude exited %d: stderr=%s", returncode, stderr[:500])
        error_msg = stderr or "claude exited with code %d" % returncode
        return {
            "response": "Error: %s" % error_msg,
            "session_id": session_id,
            "cost_usd": 0, "duration_ms": 0, "num_turns": 0,
            "tools": state["tools"],
            "rex_user_sends": rex_user_sends,
        }

    return {
        "response": state["response_text"],
        "session_id": state["session_id"],
        "cost_usd": state["cost"],
        "duration_ms": state["duration"],
        "num_turns": state["turns"],
        "tools": state["tools"],
        "rex_user_sends": rex_user_sends,
    }
