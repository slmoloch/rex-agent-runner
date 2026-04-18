#!/usr/bin/env python3
"""Telegram bot that runs Claude Code CLI as the backend.
Also listens on a local HTTP port for job submissions and a web dashboard."""
from __future__ import annotations

import asyncio
import json
import logging
from datetime import datetime, timedelta
import logging.handlers
import os
from pathlib import Path
from aiohttp import web
from telegram import Update
from telegram.ext import (
    ApplicationBuilder,
    CommandHandler,
    ContextTypes,
    MessageHandler,
    filters,
)
from claude_runner import CONFIG, WORKDIR, INSTALL_DIR, run_claude
from rex_session import (
    MAIN_SESSION,
    get_main_session_id,
    set_main_session_id,
    reset_main_session,
    resolve_session_id,
    register_session,
    get_tracked_sessions,
)
from rex_events import append_event
from rex_timeline import init_db, query_events
from rex_gc import mark_running, mark_stopped, run_gc_loop, is_running

_BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
_LOG_DIR = _BASE_DIR / "logs"
_LOG_DIR.mkdir(exist_ok=True)

_fmt = logging.Formatter("%(asctime)s - %(name)s - %(levelname)s - %(message)s")

# Hourly rotating file handler — keeps 168 files (7 days)
_file_handler = logging.handlers.TimedRotatingFileHandler(
    _LOG_DIR / "rex.log",
    when="H",
    interval=1,
    backupCount=168,
    utc=False,
)
_file_handler.setFormatter(_fmt)

# Console handler for stdout (captured by launchd / systemd)
_console_handler = logging.StreamHandler()
_console_handler.setFormatter(_fmt)

logging.basicConfig(level=logging.INFO, handlers=[_file_handler, _console_handler])
logging.getLogger("httpx").setLevel(logging.WARNING)
logging.getLogger("telegram").setLevel(logging.WARNING)
logger = logging.getLogger(__name__)

TELEGRAM_TOKEN = CONFIG["telegram_bot_token"]
ALLOWED_USER_IDS = set(CONFIG.get("allowed_user_ids", []))
JOB_PORT = CONFIG.get("job_port", 9821)

WEB_DIR = INSTALL_DIR / "web"
INBOX_DIR = Path(WORKDIR) / "inbox"


def is_authorized(update):
    user_id = update.effective_user.id
    if user_id not in ALLOWED_USER_IDS:
        logger.warning("Unauthorized access from user %d", user_id)
        return False
    return True


def _run_in_session(prompt, session_target, trigger, timeout=600, caller_session=None):
    """Run a Claude prompt in the given session target and log the event.

    session_target: "main" (persistent), "new" (ephemeral), or a raw session ID.
    trigger: source of the prompt (e.g. "telegram", "callback:name", "dispatch").
    caller_session: session ID of the caller (for dispatch tracking).
    """
    session_id = resolve_session_id(session_target)

    if session_id:
        mark_running(session_id)
    try:
        result = run_claude(prompt, session_id=session_id, timeout=timeout)
    finally:
        if session_id:
            mark_stopped(session_id)

    new_session_id = result["session_id"]

    if session_target == MAIN_SESSION and new_session_id:
        # Only update main if it hasn't been replaced underneath us
        # (e.g. a concurrent /new or daily-reset that wiped it). Without
        # this guard, a long-running turn can resurrect the old session
        # id after the reset has already installed a fresh one.
        if get_main_session_id() == session_id:
            set_main_session_id(new_session_id)

    # Register the session so the GC can track it.
    if new_session_id:
        name = session_target if session_target == MAIN_SESSION else None
        register_session(new_session_id, name=name)

    event = {
        "session": session_target,
        "session_id": new_session_id,
        "trigger": trigger,
        "prompt_preview": prompt[:200],
        "response_preview": result["response"][:500],
        "cost_usd": result["cost_usd"],
        "duration_ms": result["duration_ms"],
        "num_turns": result["num_turns"],
    }
    if caller_session:
        event["caller_session"] = caller_session
    append_event(event)

    return result["response"]


async def start(update, context):
    if not is_authorized(update):
        return
    await update.message.reply_text(
        "Hello! I'm a Claude Code bot. Send me a message and I'll process it "
        "through Claude Code.\n\n"
        "/new - Start a fresh conversation"
    )


async def new_conversation(update, context):
    if not is_authorized(update):
        return

    chat = update.effective_chat
    main_session_id = get_main_session_id()

    # Let the current session save context before wiping it.
    if main_session_id:
        typing_task = asyncio.create_task(_keep_typing(chat))
        try:
            loop = asyncio.get_event_loop()
            await loop.run_in_executor(
                None,
                lambda: _run_in_session(
                    PREPARE_RESET_PROMPT, MAIN_SESSION,
                    trigger="reset-prepare", timeout=300,
                ),
            )
        except Exception:
            logger.exception("/new: preparation prompt failed")
        finally:
            typing_task.cancel()

    reset_main_session()
    await update.message.reply_text("Session reset. Send a message to start fresh.")


async def handle_message(update, context):
    if not is_authorized(update):
        return

    chat = update.effective_chat

    # Telegram's typing indicator expires after ~5s; re-send it every 4s
    # until the Claude run finishes.
    typing_task = asyncio.create_task(_keep_typing(chat))
    try:
        loop = asyncio.get_event_loop()
        response = await loop.run_in_executor(
            None, lambda: _run_in_session(
                update.message.text, MAIN_SESSION, trigger="telegram", timeout=300
            )
        )
    except Exception:
        logger.exception("handle_message failed")
        response = "Sorry, something went wrong while processing your message."
    finally:
        typing_task.cancel()

    await _send_response(update.message, response)


async def _send_response(message, response):
    if len(response) <= 4096:
        await message.reply_text(response)
    else:
        for i in range(0, len(response), 4096):
            await message.reply_text(response[i : i + 4096])


def _pick_file(message):
    """Return (telegram file-like object, suggested filename) or (None, None)."""
    if message.document:
        doc = message.document
        return doc, doc.file_name or "document-%s" % doc.file_unique_id
    if message.photo:
        photo = message.photo[-1]  # largest size
        return photo, "photo-%s.jpg" % photo.file_unique_id
    if message.voice:
        voice = message.voice
        return voice, "voice-%s.ogg" % voice.file_unique_id
    if message.audio:
        audio = message.audio
        return audio, audio.file_name or "audio-%s.mp3" % audio.file_unique_id
    if message.video:
        video = message.video
        return video, video.file_name or "video-%s.mp4" % video.file_unique_id
    if message.video_note:
        vn = message.video_note
        return vn, "video-note-%s.mp4" % vn.file_unique_id
    if message.animation:
        anim = message.animation
        return anim, anim.file_name or "animation-%s.mp4" % anim.file_unique_id
    return None, None


async def handle_file(update, context):
    if not is_authorized(update):
        return

    message = update.message
    chat = update.effective_chat

    tg_obj, suggested_name = _pick_file(message)
    if tg_obj is None:
        await message.reply_text("Unsupported attachment type.")
        return

    INBOX_DIR.mkdir(parents=True, exist_ok=True)
    safe_name = Path(suggested_name).name
    timestamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    dest = INBOX_DIR / ("%s-%s" % (timestamp, safe_name))

    try:
        tg_file = await tg_obj.get_file()
        await tg_file.download_to_drive(custom_path=dest)
    except Exception:
        logger.exception("Failed to download file from Telegram")
        await message.reply_text("Sorry, I couldn't download that file.")
        return

    try:
        rel_path = dest.relative_to(Path(WORKDIR))
    except ValueError:
        rel_path = dest
    caption = (message.caption or "").strip()
    size = dest.stat().st_size

    prompt_lines = [
        "The user sent a file via Telegram.",
        "Path (relative to workspace): %s" % rel_path,
        "Size: %d bytes" % size,
    ]
    if caption:
        prompt_lines.append("Caption: %s" % caption)
    else:
        prompt_lines.append(
            "No caption was provided. Inspect the file if appropriate, "
            "then acknowledge receipt and ask what to do with it."
        )
    prompt = "\n".join(prompt_lines)

    typing_task = asyncio.create_task(_keep_typing(chat))
    try:
        loop = asyncio.get_event_loop()
        response = await loop.run_in_executor(
            None, lambda: _run_in_session(
                prompt, MAIN_SESSION, trigger="telegram-file", timeout=300
            )
        )
    except Exception:
        logger.exception("handle_file failed")
        response = "Sorry, something went wrong while processing your file."
    finally:
        typing_task.cancel()

    await _send_response(message, response)


async def _keep_typing(chat):
    """Send 'typing' action every 4 seconds until cancelled."""
    try:
        while True:
            await chat.send_action("typing")
            await asyncio.sleep(4)
    except asyncio.CancelledError:
        pass


# --- HTTP endpoints ---

async def handle_job_request(request):
    try:
        data = await request.json()
    except json.JSONDecodeError:
        return web.json_response({"error": "invalid json"}, status=400)

    prompt = data.get("prompt", "").strip()
    job_name = data.get("job_name", "unknown")
    session_target = data.get("session", MAIN_SESSION)
    caller_session = data.get("caller_session")
    if not prompt:
        return web.json_response({"error": "prompt required"}, status=400)

    # Inject caller session ID so the target session can report back
    if caller_session:
        prompt = "%s\n\nCaller session ID: %s\nTo report results back to the caller, run: rex dispatch %s \"<your response>\"" % (
            prompt, caller_session, caller_session,
        )

    logger.info("Job received: %s (session: %s)", job_name, session_target)

    loop = asyncio.get_event_loop()
    response = await loop.run_in_executor(
        None, lambda: _run_in_session(
            prompt, session_target, trigger=job_name,
            caller_session=caller_session,
        )
    )

    logger.info("Job %s finished. Response: %s", job_name, response[:500])
    return web.json_response({"status": "ok", "response": response[:1000]})


async def handle_events_api(request):
    since = request.query.get('since')
    events = query_events(days=7, since=since)
    return web.json_response(events)


async def handle_sessions_api(request):
    tracked = get_tracked_sessions()
    main_id = get_main_session_id()
    sessions = []
    for sid, info in tracked.items():
        sessions.append({
            "session_id": sid,
            "name": info.get("name"),
            "last_activity": info.get("last_activity"),
            "is_main": sid == main_id,
            "is_running": is_running(sid),
        })
    sessions.sort(key=lambda s: s.get("last_activity", ""), reverse=True)
    return web.json_response(sessions)


async def handle_dashboard(request):
    index_path = WEB_DIR / "index.html"
    if not index_path.exists():
        return web.Response(text="Dashboard not found.", status=404)
    return web.FileResponse(index_path)


async def handle_timeline(request):
    path = WEB_DIR / "timeline.html"
    if not path.exists():
        return web.Response(text="Timeline not found.", status=404)
    return web.FileResponse(path)


async def run_http_server():
    app = web.Application()
    app.router.add_get("/", handle_dashboard)
    app.router.add_get("/timeline", handle_timeline)
    app.router.add_static("/static", WEB_DIR, show_index=False)
    app.router.add_get("/api/events", handle_events_api)
    app.router.add_get("/api/sessions", handle_sessions_api)
    app.router.add_post("/job", handle_job_request)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", JOB_PORT)
    await site.start()
    logger.info("Dashboard: http://127.0.0.1:%d/", JOB_PORT)


DAILY_RESET_POLL_SECONDS = 10  # how often to check if session is inactive

PREPARE_RESET_PROMPT = (
    "Heads up: your session is about to be reset."
)


async def run_daily_reset_loop():
    """Background task that resets the main session at midnight each day.

    If the main session is active at midnight, waits until it becomes
    inactive before performing the reset.  After resetting, starts a new
    session and informs the agent about the daily reset and current date.
    """
    logger.info("Daily session reset scheduler started.")
    while True:
        # Sleep until next midnight.
        now = datetime.now()
        tomorrow = (now + timedelta(days=1)).replace(
            hour=0, minute=0, second=0, microsecond=0
        )
        seconds_until_midnight = (tomorrow - now).total_seconds()
        logger.info(
            "Daily reset: next reset in %.0f seconds (at %s)",
            seconds_until_midnight,
            tomorrow.isoformat(),
        )
        await asyncio.sleep(seconds_until_midnight)

        # Wait until the main session is no longer active.
        main_session_id = get_main_session_id()
        if main_session_id and is_running(main_session_id):
            logger.info("Daily reset: main session is active, waiting…")
            while main_session_id and is_running(main_session_id):
                await asyncio.sleep(DAILY_RESET_POLL_SECONDS)
                main_session_id = get_main_session_id()
            logger.info("Daily reset: main session is now inactive.")

        # Tell the current session to prepare for the reset.
        main_session_id = get_main_session_id()
        if main_session_id:
            try:
                loop = asyncio.get_event_loop()
                await loop.run_in_executor(
                    None,
                    lambda: _run_in_session(
                        PREPARE_RESET_PROMPT, MAIN_SESSION,
                        trigger="daily-reset-prepare", timeout=300,
                    ),
                )
                logger.info("Daily reset: preparation prompt completed.")
            except Exception:
                logger.exception("Daily reset: preparation prompt failed")

        # Reset the main session.
        reset_main_session()
        logger.info("Daily reset: main session has been reset.")

        # Start a new session and tell the agent about the reset.
        today = datetime.now().strftime("%Y-%m-%d")
        reset_prompt = (
            "Your session has been reset (daily midnight reset). "
            "Today's date is %s." % today
        )

        try:
            loop = asyncio.get_event_loop()
            await loop.run_in_executor(
                None,
                lambda: _run_in_session(
                    reset_prompt, MAIN_SESSION,
                    trigger="daily-reset", timeout=300,
                ),
            )
            logger.info("Daily reset: new session initialized successfully.")
        except Exception:
            logger.exception("Daily reset: failed to initialize new session")


async def error_handler(update, context):
    """Global error handler for the Telegram application."""
    logger.error("Unhandled exception: %s", context.error, exc_info=context.error)
    if update and update.effective_chat:
        try:
            await update.effective_chat.send_message(
                "Sorry, something went wrong. Please try again."
            )
        except Exception:
            logger.exception("Failed to send error message to user")


async def post_init(application):
    init_db()
    await run_http_server()
    asyncio.create_task(run_gc_loop())
    asyncio.create_task(run_daily_reset_loop())


def main():
    app = ApplicationBuilder().token(TELEGRAM_TOKEN).post_init(post_init).build()

    app.add_handler(CommandHandler("start", start))
    app.add_handler(CommandHandler("new", new_conversation))
    app.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, handle_message))
    app.add_handler(MessageHandler(filters.ATTACHMENT & ~filters.COMMAND, handle_file))
    app.add_error_handler(error_handler)

    logger.info("Bot started.")
    app.run_polling(drop_pending_updates=True)


if __name__ == "__main__":
    main()
