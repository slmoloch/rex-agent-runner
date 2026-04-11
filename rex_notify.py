#!/usr/bin/env python3
"""Send a Telegram notification to the user."""
from __future__ import annotations

import json
import ssl
import sys
import urllib.request
import urllib.parse
from pathlib import Path

try:
    import certifi
    SSL_CONTEXT = ssl.create_default_context(cafile=certifi.where())
except ImportError:
    SSL_CONTEXT = None

import os

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
CONFIG_PATH = BASE_DIR / "config.json"

with open(CONFIG_PATH) as f:
    CONFIG = json.load(f)

TELEGRAM_TOKEN = CONFIG["telegram_bot_token"]
CHAT_ID = CONFIG.get("telegram_chat_id", CONFIG["allowed_user_ids"][0])


def send(message):
    url = "https://api.telegram.org/bot%s/sendMessage" % TELEGRAM_TOKEN
    data = urllib.parse.urlencode({
        "chat_id": CHAT_ID,
        "text": message,
        "parse_mode": "Markdown",
    }).encode()
    try:
        req = urllib.request.Request(url, data=data)
        with urllib.request.urlopen(req, context=SSL_CONTEXT) as resp:
            result = json.loads(resp.read())
            if result.get("ok"):
                print("Message sent to chat %s" % CHAT_ID)
            else:
                print("Telegram error: %s" % result, file=sys.stderr)
                sys.exit(1)
    except Exception as e:
        print("Failed to send message: %s" % e, file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: rex notify <message>")
        sys.exit(1)
    send(" ".join(sys.argv[1:]))
