#!/usr/bin/env python3
"""Telegram bot that runs Claude Code CLI as the backend.
Also listens on a local HTTP port for job submissions and a web dashboard."""
from __future__ import annotations

import asyncio
import json
import logging
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
)
from rex_events import append_event, load_events

logging.basicConfig(
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
    level=logging.INFO,
)
logging.getLogger("httpx").setLevel(logging.WARNING)
logging.getLogger("telegram").setLevel(logging.WARNING)
logger = logging.getLogger(__name__)

TELEGRAM_TOKEN = CONFIG["telegram_bot_token"]
ALLOWED_USER_IDS = set(CONFIG.get("allowed_user_ids", []))
JOB_PORT = CONFIG.get("job_port", 9821)

WEB_DIR = INSTALL_DIR / "web"


def is_authorized(update):
    user_id = update.effective_user.id
    if user_id not in ALLOWED_USER_IDS:
        logger.warning("Unauthorized access from user %d", user_id)
        return False
    return True


def _run_in_session(prompt, session_target, trigger, timeout=600):
    """Run a Claude prompt in the given session target and log the event.

    session_target: "main" (persistent), "new" (ephemeral), or a raw session ID.
    trigger: source of the prompt (e.g. "telegram", "callback:name", "dispatch").
    """
    session_id = resolve_session_id(session_target)
    result = run_claude(prompt, session_id=session_id, timeout=timeout)

    if session_target == MAIN_SESSION and result["session_id"]:
        set_main_session_id(result["session_id"])

    append_event({
        "session": session_target,
        "session_id": result["session_id"],
        "trigger": trigger,
        "prompt_preview": prompt[:200],
        "response_preview": result["response"][:500],
        "cost_usd": result["cost_usd"],
        "duration_ms": result["duration_ms"],
        "num_turns": result["num_turns"],
    })

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
    reset_main_session()
    await update.message.reply_text("Session reset. Send a message to start fresh.")


async def handle_message(update, context):
    if not is_authorized(update):
        return

    await update.effective_chat.send_action("typing")

    loop = asyncio.get_event_loop()
    response = await loop.run_in_executor(
        None, lambda: _run_in_session(
            update.message.text, MAIN_SESSION, trigger="telegram", timeout=300
        )
    )

    if len(response) <= 4096:
        await update.message.reply_text(response)
    else:
        for i in range(0, len(response), 4096):
            await update.message.reply_text(response[i : i + 4096])


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
        None, lambda: _run_in_session(prompt, session_target, trigger=job_name)
    )

    logger.info("Job %s finished. Response: %s", job_name, response[:500])
    return web.json_response({"status": "ok", "response": response[:1000]})


async def handle_events_api(request):
    events = load_events(days=7)
    return web.json_response(events)


async def handle_dashboard(request):
    index_path = WEB_DIR / "index.html"
    if not index_path.exists():
        return web.Response(text="Dashboard not found.", status=404)
    return web.FileResponse(index_path)


async def run_http_server():
    app = web.Application()
    app.router.add_get("/", handle_dashboard)
    app.router.add_get("/api/events", handle_events_api)
    app.router.add_post("/job", handle_job_request)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", JOB_PORT)
    await site.start()
    logger.info("Dashboard: http://127.0.0.1:%d/", JOB_PORT)


async def post_init(application):
    await run_http_server()


def main():
    app = ApplicationBuilder().token(TELEGRAM_TOKEN).post_init(post_init).build()

    app.add_handler(CommandHandler("start", start))
    app.add_handler(CommandHandler("new", new_conversation))
    app.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, handle_message))

    logger.info("Bot started.")
    app.run_polling(drop_pending_updates=True)


if __name__ == "__main__":
    main()
