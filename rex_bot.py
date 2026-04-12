#!/usr/bin/env python3
"""Telegram bot that runs Claude Code CLI as the backend.
Also listens on a local HTTP port for job submissions."""
from __future__ import annotations

import asyncio
import json
import logging
from aiohttp import web
from telegram import Update
from telegram.ext import (
    ApplicationBuilder,
    CommandHandler,
    ContextTypes,
    MessageHandler,
    filters,
)
from claude_runner import CONFIG, WORKDIR, run_claude
from rex_session import (
    MAIN_SESSION,
    get_session_id,
    set_session_id,
    reset_session,
)

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


def is_authorized(update):
    user_id = update.effective_user.id
    if user_id not in ALLOWED_USER_IDS:
        logger.warning("Unauthorized access from user %d", user_id)
        return False
    return True


def _run_in_session(prompt, session_name, timeout=600):
    """Run a Claude prompt in the given named session.

    session_name: "main", a custom name (persisted), or "new" (ephemeral).
    Returns (response_text, session_name).
    """
    if session_name == "new":
        response, _ = run_claude(prompt, session_id=None, timeout=timeout)
        return response

    session_id = get_session_id(session_name)
    response, new_id = run_claude(prompt, session_id=session_id, timeout=timeout)
    if new_id:
        set_session_id(session_name, new_id)
    return response


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
    reset_session(MAIN_SESSION)
    await update.message.reply_text("Session reset. Send a message to start fresh.")


async def handle_message(update, context):
    if not is_authorized(update):
        return

    await update.effective_chat.send_action("typing")

    loop = asyncio.get_event_loop()
    response = await loop.run_in_executor(
        None, lambda: _run_in_session(update.message.text, MAIN_SESSION, timeout=300)
    )

    if len(response) <= 4096:
        await update.message.reply_text(response)
    else:
        for i in range(0, len(response), 4096):
            await update.message.reply_text(response[i : i + 4096])


# --- Job HTTP endpoint ---

async def handle_job_request(request):
    try:
        data = await request.json()
    except json.JSONDecodeError:
        return web.json_response({"error": "invalid json"}, status=400)

    prompt = data.get("prompt", "").strip()
    job_name = data.get("job_name", "unknown")
    session_name = data.get("session", MAIN_SESSION)
    if not prompt:
        return web.json_response({"error": "prompt required"}, status=400)

    logger.info("Job received: %s (session: %s)", job_name, session_name)

    loop = asyncio.get_event_loop()
    response = await loop.run_in_executor(
        None, lambda: _run_in_session(prompt, session_name)
    )

    logger.info("Job %s finished. Response: %s", job_name, response[:500])
    return web.json_response({"status": "ok", "response": response[:1000]})


async def run_http_server():
    app = web.Application()
    app.router.add_post("/job", handle_job_request)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", JOB_PORT)
    await site.start()
    logger.info("Job server listening on http://127.0.0.1:%d/job", JOB_PORT)


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
