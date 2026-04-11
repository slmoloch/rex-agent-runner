#!/usr/bin/env python3
"""Telegram bot that runs Claude Code CLI as the backend.
Also listens on a local HTTP port for job submissions."""
from __future__ import annotations

import asyncio
import json
import logging
from aiohttp import web
from datetime import datetime
from pathlib import Path
from telegram import Update, Bot
from telegram.ext import (
    ApplicationBuilder,
    CommandHandler,
    ContextTypes,
    MessageHandler,
    filters,
)
from claude_runner import CONFIG, WORKDIR, run_claude

logging.basicConfig(
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
    level=logging.INFO,
)
logging.getLogger("httpx").setLevel(logging.WARNING)
logging.getLogger("telegram").setLevel(logging.WARNING)
logger = logging.getLogger(__name__)

TELEGRAM_TOKEN = CONFIG["telegram_bot_token"]
ALLOWED_USER_IDS: set[int] = set(CONFIG.get("allowed_user_ids", []))
CHAT_ID = CONFIG.get("telegram_chat_id", CONFIG["allowed_user_ids"][0])
JOB_PORT = CONFIG.get("job_port", 9821)

# Track session IDs per chat so conversations persist
session_history: dict[int, list[dict]] = {}
active_session: dict[int, int] = {}

bot: Bot | None = None


def is_authorized(update: Update) -> bool:
    user_id = update.effective_user.id
    if user_id not in ALLOWED_USER_IDS:
        logger.warning("Unauthorized access from user %d", user_id)
        return False
    return True


def get_active_session_id(chat_id: int) -> str | None:
    idx = active_session.get(chat_id)
    if idx is not None and chat_id in session_history:
        return session_history[chat_id][idx]["id"]
    return None


async def start(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not is_authorized(update):
        return
    await update.message.reply_text(
        "Hello! I'm a Claude Code bot. Send me a message and I'll process it "
        "through Claude Code.\n\n"
        "/new [name] - Start a fresh conversation\n"
        "/sessions - List all sessions\n"
        "/switch <number> - Switch to a session"
    )


async def new_conversation(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not is_authorized(update):
        return
    chat_id = update.effective_chat.id
    name = " ".join(context.args) if context.args else None

    if chat_id not in session_history:
        session_history[chat_id] = []

    entry = {
        "id": None,
        "name": name or f"Session {len(session_history[chat_id]) + 1}",
        "created": datetime.now().strftime("%Y-%m-%d %H:%M"),
    }
    session_history[chat_id].append(entry)
    active_session[chat_id] = len(session_history[chat_id]) - 1

    await update.message.reply_text(f"New session started: *{entry['name']}*", parse_mode="Markdown")


async def list_sessions(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not is_authorized(update):
        return
    chat_id = update.effective_chat.id
    history = session_history.get(chat_id, [])

    if not history:
        await update.message.reply_text("No sessions yet. Send a message to start one.")
        return

    current_idx = active_session.get(chat_id)
    lines = []
    for i, s in enumerate(history):
        marker = " <<" if i == current_idx else ""
        lines.append(f"{i + 1}. *{s['name']}* ({s['created']}){marker}")

    await update.message.reply_text("Sessions:\n" + "\n".join(lines), parse_mode="Markdown")


async def switch_session(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not is_authorized(update):
        return
    chat_id = update.effective_chat.id
    history = session_history.get(chat_id, [])

    if not context.args:
        await update.message.reply_text("Usage: /switch <number>")
        return

    try:
        idx = int(context.args[0]) - 1
    except ValueError:
        await update.message.reply_text("Please provide a session number.")
        return

    if idx < 0 or idx >= len(history):
        await update.message.reply_text(f"Invalid session. Use 1-{len(history)}.")
        return

    active_session[chat_id] = idx
    await update.message.reply_text(
        f"Switched to: *{history[idx]['name']}*", parse_mode="Markdown"
    )


async def handle_message(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if not is_authorized(update):
        return
    chat_id = update.effective_chat.id
    user_text = update.message.text

    if chat_id not in session_history or not session_history[chat_id]:
        session_history[chat_id] = [{
            "id": None,
            "name": "Session 1",
            "created": datetime.now().strftime("%Y-%m-%d %H:%M"),
        }]
        active_session[chat_id] = 0

    await update.effective_chat.send_action("typing")

    session_id = get_active_session_id(chat_id)
    response, new_session_id = run_claude(user_text, session_id)

    if new_session_id:
        idx = active_session[chat_id]
        session_history[chat_id][idx]["id"] = new_session_id

    if len(response) <= 4096:
        await update.message.reply_text(response)
    else:
        for i in range(0, len(response), 4096):
            await update.message.reply_text(response[i : i + 4096])


# --- Job HTTP endpoint ---

async def handle_job_request(request: web.Request) -> web.Response:
    """Handle job submissions via HTTP POST."""
    try:
        data = await request.json()
    except json.JSONDecodeError:
        return web.json_response({"error": "invalid json"}, status=400)

    prompt = data.get("prompt", "").strip()
    job_name = data.get("job_name", "unknown")
    if not prompt:
        return web.json_response({"error": "prompt required"}, status=400)

    logger.info("Job received: %s", job_name)

    # Always start a fresh session for jobs
    loop = asyncio.get_event_loop()
    response, _ = await loop.run_in_executor(None, lambda: run_claude(prompt, timeout=600))

    logger.info("Job %s finished. Response: %s", job_name, response[:500])

    # Send result to user via Telegram
    if bot and response:
        msg = f"*Job: {job_name}*\n\n{response}"
        try:
            if len(msg) <= 4096:
                await bot.send_message(chat_id=CHAT_ID, text=msg, parse_mode="Markdown")
            else:
                for i in range(0, len(msg), 4096):
                    await bot.send_message(chat_id=CHAT_ID, text=msg[i:i+4096])
        except Exception as e:
            logger.error("Failed to send job result via Telegram: %s", e)

    return web.json_response({"status": "ok", "response": response[:1000]})


async def run_http_server():
    """Run the HTTP server for job submissions."""
    app = web.Application()
    app.router.add_post("/job", handle_job_request)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", JOB_PORT)
    await site.start()
    logger.info("Job server listening on http://127.0.0.1:%d/job", JOB_PORT)


def main() -> None:
    global bot

    app = ApplicationBuilder().token(TELEGRAM_TOKEN).build()
    bot = app.bot

    app.add_handler(CommandHandler("start", start))
    app.add_handler(CommandHandler("new", new_conversation))
    app.add_handler(CommandHandler("sessions", list_sessions))
    app.add_handler(CommandHandler("switch", switch_session))
    app.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, handle_message))

    # Start HTTP server in the same event loop
    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    loop.run_until_complete(run_http_server())

    logger.info("Bot started.")
    app.run_polling(drop_pending_updates=True)


if __name__ == "__main__":
    main()
