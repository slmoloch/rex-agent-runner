"""Shared Claude Code CLI runner.

Keeps a pool of persistent Claude Code subprocesses (one per session) to avoid
the startup overhead of spawning ``claude -p`` for every single message.
Falls back to the traditional one-shot approach when a persistent process dies.
"""

from __future__ import annotations

import json
import logging
import os
import queue
import shutil
import subprocess
import threading
import time
import uuid
from pathlib import Path

logger = logging.getLogger(__name__)

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
CONFIG_PATH = BASE_DIR / "config.json"

with open(CONFIG_PATH) as f:
    CONFIG = json.load(f)

WORKDIR = str((BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve())

DISALLOWED_TOOLS = "CronCreate,CronDelete,CronList,TaskCreate,TaskGet,TaskList,TaskUpdate,TodoWrite"


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


# ---------------------------------------------------------------------------
# Persistent process approach
# ---------------------------------------------------------------------------

class ProcessDiedError(RuntimeError):
    """Raised when a persistent Claude process has exited unexpectedly."""


class ProcessBusyError(RuntimeError):
    """Raised when the persistent process is already handling another prompt.

    This happens during dispatch-back: Session A dispatches to B, and B
    dispatches back to A while A's process is still locked.  The caller
    should fall back to a one-shot subprocess without removing the busy
    process from the pool.
    """


class ClaudeProcess:
    """A persistent Claude Code subprocess for a single session.

    Uses ``--input-format stream-json`` / ``--output-format stream-json`` to
    keep the process alive across multiple prompts, avoiding the startup
    overhead of spawning a new ``claude -p`` for every message.
    """

    def __init__(self, session_id: str, system_prompt: str | None = None,
                 is_new: bool = False):
        self.session_id = session_id
        self.is_new = is_new
        self.process: subprocess.Popen | None = None
        self.lock = threading.Lock()
        self.last_used = time.time()
        self._stdout_queue: queue.Queue[str | None] = queue.Queue()
        self._reader_thread: threading.Thread | None = None
        self._start(system_prompt)

    def _start(self, system_prompt: str | None = None) -> None:
        cmd = [
            CLAUDE_BIN, "-p",
            "--input-format", "stream-json",
            "--output-format", "stream-json",
            "--verbose",
            "--dangerously-skip-permissions",
            "--disallowedTools", DISALLOWED_TOOLS,
        ]

        if self.is_new:
            cmd.extend(["--session-id", self.session_id])
            if system_prompt or SYSTEM_PROMPT:
                cmd.extend(["--system-prompt", system_prompt or SYSTEM_PROMPT])
        else:
            cmd.extend(["-r", self.session_id])

        env = os.environ.copy()
        env["REX_SESSION_ID"] = self.session_id

        self.process = subprocess.Popen(
            cmd,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            cwd=WORKDIR,
            env=env,
        )

        self._reader_thread = threading.Thread(target=self._read_stdout,
                                               daemon=True)
        self._reader_thread.start()

        logger.info("Started persistent Claude process for session %s "
                     "(pid=%d, new=%s)",
                     self.session_id, self.process.pid, self.is_new)

    # -- background reader ---------------------------------------------------

    def _read_stdout(self) -> None:
        """Background thread: read lines from stdout into a queue."""
        try:
            assert self.process is not None
            for line in self.process.stdout:
                self._stdout_queue.put(line.rstrip("\n"))
        except Exception:
            pass
        self._stdout_queue.put(None)  # EOF sentinel

    # -- public API -----------------------------------------------------------

    def send_message(self, prompt: str, timeout: int = 300) -> dict:
        """Send a user message and block until the full response arrives."""
        if not self.lock.acquire(timeout=5):
            raise ProcessBusyError(
                "Process for session %s is busy with another prompt"
                % self.session_id)
        try:
            if not self.is_alive():
                raise ProcessDiedError(
                    "Claude process exited (pid was %s)"
                    % (self.process.pid if self.process else "?"))

            # Update timestamp before sending so the cleanup loop won't kill
            # a process that's in the middle of a long-running prompt.
            self.last_used = time.time()

            msg = json.dumps({"type": "user_input", "content": prompt})
            try:
                self.process.stdin.write(msg + "\n")
                self.process.stdin.flush()
            except (BrokenPipeError, OSError) as exc:
                raise ProcessDiedError(
                    "Failed to write to Claude stdin: %s" % exc)

            logger.info("Prompt (persistent): %s", prompt[:200])
            return self._read_response(timeout)
        finally:
            self.lock.release()

    def _read_response(self, timeout: int) -> dict:
        response_text = ""
        new_session_id = self.session_id
        cost = 0.0
        turns = 0
        duration = 0

        deadline = time.time() + timeout

        while True:
            remaining = deadline - time.time()
            if remaining <= 0:
                raise TimeoutError("Prompt timed out after %ds" % timeout)

            try:
                line = self._stdout_queue.get(timeout=min(remaining, 1.0))
            except queue.Empty:
                if not self.is_alive():
                    break
                continue

            if line is None:
                # Process exited mid-response
                break

            if not line.strip():
                continue

            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue

            etype = event.get("type")

            if etype == "result":
                response_text = event.get("result", "")
                new_session_id = event.get("session_id", self.session_id)
                cost = event.get("total_cost_usd", 0)
                turns = event.get("num_turns", 0)
                duration = event.get("duration_ms", 0)
                logger.info("Result (persistent): turns=%d, duration=%dms, "
                            "cost=$%.4f", turns, duration, cost)
                break

            elif etype == "assistant":
                message = event.get("message", {})
                for block in message.get("content", []):
                    if block.get("type") == "tool_use":
                        logger.info("Tool: %s(%s)", block.get("name"),
                                    json.dumps(block.get("input", {}))[:200])
                    elif block.get("type") == "text":
                        text = block.get("text", "")
                        if text:
                            logger.info("Claude: %s", text[:300])

        self.last_used = time.time()

        return {
            "response": response_text,
            "session_id": new_session_id,
            "cost_usd": cost,
            "duration_ms": duration,
            "num_turns": turns,
        }

    def is_alive(self) -> bool:
        return self.process is not None and self.process.poll() is None

    def close(self) -> None:
        if self.process is None:
            return
        pid = self.process.pid
        try:
            self.process.stdin.close()
        except Exception:
            pass
        try:
            self.process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.process.kill()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
        logger.info("Closed persistent Claude process for session %s "
                     "(pid=%d)", self.session_id, pid)
        self.process = None


class ProcessPool:
    """Pool of persistent Claude Code processes, keyed by session ID.

    Idle processes are cleaned up after *idle_timeout* seconds (default 30 min).
    """

    def __init__(self, idle_timeout: int = 1800):
        self._processes: dict[str, ClaudeProcess] = {}
        self._lock = threading.Lock()
        self._idle_timeout = idle_timeout
        self._cleanup_thread = threading.Thread(target=self._cleanup_loop,
                                                daemon=True)
        self._cleanup_thread.start()

    def get_or_create(self, session_id: str,
                      system_prompt: str | None = None,
                      is_new: bool = False) -> ClaudeProcess:
        with self._lock:
            if session_id in self._processes:
                proc = self._processes[session_id]
                if proc.is_alive():
                    return proc
                del self._processes[session_id]

            proc = ClaudeProcess(session_id, system_prompt=system_prompt,
                                 is_new=is_new)
            self._processes[session_id] = proc
            return proc

    def remove(self, session_id: str) -> None:
        with self._lock:
            proc = self._processes.pop(session_id, None)
        if proc:
            proc.close()

    def close_all(self) -> None:
        with self._lock:
            procs = list(self._processes.values())
            self._processes.clear()
        for proc in procs:
            proc.close()

    def _cleanup_loop(self) -> None:
        while True:
            time.sleep(60)
            now = time.time()
            to_close: list[tuple[str, ClaudeProcess]] = []
            with self._lock:
                for sid, proc in list(self._processes.items()):
                    if not proc.is_alive():
                        to_close.append((sid, self._processes.pop(sid)))
                    elif now - proc.last_used > self._idle_timeout:
                        to_close.append((sid, self._processes.pop(sid)))
            for sid, proc in to_close:
                logger.info("Process pool: cleaning idle/dead process for "
                            "session %s", sid)
                proc.close()


_pool = ProcessPool()


# ---------------------------------------------------------------------------
# One-shot fallback (original approach)
# ---------------------------------------------------------------------------

def _run_claude_oneshot(
    prompt: str,
    session_id: str | None = None,
    system_prompt: str | None = None,
    timeout: int = 300,
) -> dict:
    """Spawn ``claude -p`` once and wait for it to exit.

    Used as a fallback when the persistent process approach fails.
    """
    cmd = [
        CLAUDE_BIN, "-p", prompt,
        "--output-format", "stream-json", "--verbose",
        "--dangerously-skip-permissions",
        "--disallowedTools", DISALLOWED_TOOLS,
    ]

    if session_id:
        cmd.extend(["-r", session_id])
    elif system_prompt or SYSTEM_PROMPT:
        cmd.extend(["--system-prompt", system_prompt or SYSTEM_PROMPT])

    logger.info("Prompt (oneshot fallback): %s", prompt[:200])

    env = os.environ.copy()
    if session_id:
        env["REX_SESSION_ID"] = session_id

    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        cwd=WORKDIR,
        timeout=timeout,
        env=env,
    )

    if result.returncode != 0:
        stderr = result.stderr.strip()
        stdout = result.stdout.strip()
        logger.error("claude exited %d: stderr=%s stdout=%s",
                     result.returncode, stderr, stdout[:500])
        error_msg = (stderr or stdout
                     or "claude exited with code %d" % result.returncode)
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
            logger.info("Result (oneshot): turns=%d, duration=%dms, "
                        "cost=$%.4f", turns, duration, cost)

        elif etype == "assistant":
            message = event.get("message", {})
            for block in message.get("content", []):
                if block.get("type") == "tool_use":
                    logger.info("Tool: %s(%s)", block.get("name"),
                                json.dumps(block.get("input", {}))[:200])
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


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------

def run_claude(
    prompt: str,
    session_id: str | None = None,
    system_prompt: str | None = None,
    timeout: int = 300,
) -> dict:
    """Run claude CLI and return a result dict.

    For known sessions the call is routed to a persistent subprocess that
    stays alive between prompts, eliminating process-startup overhead.
    If the persistent process is unavailable the function transparently falls
    back to spawning a one-shot ``claude -p``.

    Returns: {"response": str, "session_id": str|None,
              "cost_usd": float, "duration_ms": int, "num_turns": int}
    """
    is_new = session_id is None
    effective_id = session_id or str(uuid.uuid4())

    try:
        proc = _pool.get_or_create(
            effective_id, system_prompt=system_prompt, is_new=is_new,
        )
        result = proc.send_message(prompt, timeout=timeout)
        return result
    except ProcessBusyError as exc:
        # Process is handling another prompt (e.g. dispatch-back scenario).
        # Fall back to one-shot WITHOUT removing the busy process from the pool.
        logger.info("Process busy for session %s (%s), using one-shot",
                     effective_id, exc)
        return _run_claude_oneshot(
            prompt,
            session_id=session_id,
            system_prompt=system_prompt,
            timeout=timeout,
        )
    except (ProcessDiedError, TimeoutError, BrokenPipeError, OSError) as exc:
        logger.warning("Persistent process failed for session %s (%s), "
                        "falling back to one-shot", effective_id, exc)
        _pool.remove(effective_id)
        return _run_claude_oneshot(
            prompt,
            session_id=session_id,
            system_prompt=system_prompt,
            timeout=timeout,
        )


def close_session(session_id: str) -> None:
    """Close the persistent process for a session (if any)."""
    if session_id:
        _pool.remove(session_id)


def shutdown_pool() -> None:
    """Close all persistent processes.  Call on daemon shutdown."""
    _pool.close_all()
