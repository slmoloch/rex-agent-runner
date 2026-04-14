#!/usr/bin/env python3
"""Session garbage collector.

Checks tracked sessions every 30 minutes and removes those that are:
  1. Not currently active (no prompt in flight),
  2. Have not dispatched a message to another active session, and
  3. Have no callbacks configured for them.
"""
from __future__ import annotations

import asyncio
import logging

from claude_runner import close_session
from rex_callback import _load_callbacks
from rex_events import append_event, load_events
from rex_session import (
    MAIN_SESSION,
    get_main_session_id,
    get_tracked_sessions,
    unregister_session,
)

logger = logging.getLogger(__name__)

GC_INTERVAL_SECONDS = 30 * 60  # 30 minutes

# In-memory set of session IDs that currently have a run_claude call in flight.
_running_sessions: set[str] = set()


def mark_running(session_id: str) -> None:
    """Mark a session as currently executing a prompt."""
    if session_id:
        _running_sessions.add(session_id)


def mark_stopped(session_id: str) -> None:
    """Mark a session as no longer executing."""
    _running_sessions.discard(session_id)


def is_running(session_id: str) -> bool:
    return session_id in _running_sessions


# --- GC criteria helpers ---

def _has_callbacks(session_id: str) -> bool:
    """Return True if any callback targets this session (by ID or by name)."""
    callbacks = _load_callbacks()
    main_session_id = get_main_session_id()
    for cb in callbacks.values():
        cb_session = cb.get("session", "new")
        if cb_session == session_id:
            return True
        # A callback targeting "main" keeps the main session alive.
        if cb_session == MAIN_SESSION and session_id == main_session_id:
            return True
    return False


def _dispatched_to_active(session_id: str) -> bool:
    """Return True if this session recently dispatched to another active session.

    Looks at events from the last 24 hours where *this* session was the caller
    and the target session is still tracked (i.e. still around).
    """
    events = load_events(days=1)
    tracked = get_tracked_sessions()
    for event in events:
        if event.get("caller_session") != session_id:
            continue
        target_sid = event.get("session_id")
        if not target_sid or target_sid == session_id:
            continue
        # Target is still tracked or is currently running — keep the caller.
        if target_sid in tracked or is_running(target_sid):
            return True
    return False


# --- Core GC ---

def collect() -> list[str]:
    """Run one garbage-collection pass.  Returns list of cleaned session IDs."""
    tracked = get_tracked_sessions()
    main_session_id = get_main_session_id()
    cleaned: list[str] = []

    for session_id in list(tracked):
        # Never GC the main session.
        if session_id == main_session_id:
            continue

        # Condition 1: session is currently running a prompt.
        if is_running(session_id):
            continue

        # Condition 2: session dispatched to another active session.
        if _dispatched_to_active(session_id):
            continue

        # Condition 3: session has callbacks set up.
        if _has_callbacks(session_id):
            continue

        # All three conditions failed — collect this session.
        info = tracked[session_id]
        logger.info(
            "GC: collecting session %s (name=%s, last_activity=%s)",
            session_id, info.get("name"), info.get("last_activity"),
        )
        unregister_session(session_id)
        close_session(session_id)
        cleaned.append(session_id)

        append_event({
            "session": info.get("name") or session_id,
            "session_id": session_id,
            "trigger": "gc",
            "prompt_preview": "Session garbage collected",
            "response_preview": "last_activity=%s" % info.get("last_activity", "?"),
            "cost_usd": 0,
            "duration_ms": 0,
            "num_turns": 0,
        })

    return cleaned


async def run_gc_loop() -> None:
    """Background asyncio task that runs GC every 30 minutes."""
    logger.info("Session GC started (interval=%ds).", GC_INTERVAL_SECONDS)
    while True:
        await asyncio.sleep(GC_INTERVAL_SECONDS)
        try:
            cleaned = collect()
            if cleaned:
                logger.info("GC: cleaned %d session(s): %s", len(cleaned), cleaned)
            else:
                logger.debug("GC: no sessions to clean.")
        except Exception:
            logger.exception("GC: error during collection")
