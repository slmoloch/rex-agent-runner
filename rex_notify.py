#!/usr/bin/env python3
"""Send a Telegram notification to the user."""
from __future__ import annotations

import json
import mimetypes
import os
import ssl
import sys
import uuid
import urllib.request
import urllib.parse
from pathlib import Path

try:
    import certifi
    SSL_CONTEXT = ssl.create_default_context(cafile=certifi.where())
except ImportError:
    SSL_CONTEXT = None

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
CONFIG_PATH = BASE_DIR / "config.json"

with open(CONFIG_PATH) as f:
    CONFIG = json.load(f)

TELEGRAM_TOKEN = CONFIG["telegram_bot_token"]
CHAT_ID = CONFIG.get("telegram_chat_id", CONFIG["allowed_user_ids"][0])


def _build_multipart(fields, files):
    """Build a multipart/form-data body from fields and files.

    fields: dict of {name: value}
    files: dict of {name: (filename, data, content_type)}

    Returns (body_bytes, content_type_header).
    """
    boundary = uuid.uuid4().hex
    lines = []
    for key, value in fields.items():
        lines.append(("--%s" % boundary).encode())
        lines.append(('Content-Disposition: form-data; name="%s"' % key).encode())
        lines.append(b"")
        lines.append(str(value).encode())
    for key, (filename, data, content_type) in files.items():
        lines.append(("--%s" % boundary).encode())
        lines.append(
            ('Content-Disposition: form-data; name="%s"; filename="%s"' % (key, filename)).encode()
        )
        lines.append(("Content-Type: %s" % content_type).encode())
        lines.append(b"")
        lines.append(data)
    lines.append(("--%s--" % boundary).encode())
    body = b"\r\n".join(lines)
    content_type = "multipart/form-data; boundary=%s" % boundary
    return body, content_type


def send(message):
    """Send a text message to Telegram."""
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


def send_file(file_path, caption=None):
    """Send a file to Telegram via sendDocument."""
    file_path = Path(file_path)
    if not file_path.exists():
        print("File not found: %s" % file_path, file=sys.stderr)
        sys.exit(1)

    url = "https://api.telegram.org/bot%s/sendDocument" % TELEGRAM_TOKEN
    content_type = mimetypes.guess_type(str(file_path))[0] or "application/octet-stream"

    fields = {"chat_id": CHAT_ID}
    if caption:
        fields["caption"] = caption
        fields["parse_mode"] = "Markdown"

    with open(file_path, "rb") as f:
        file_data = f.read()

    files = {"document": (file_path.name, file_data, content_type)}
    body, multipart_ct = _build_multipart(fields, files)

    try:
        req = urllib.request.Request(url, data=body)
        req.add_header("Content-Type", multipart_ct)
        with urllib.request.urlopen(req, context=SSL_CONTEXT) as resp:
            result = json.loads(resp.read())
            if result.get("ok"):
                print("File sent to chat %s: %s" % (CHAT_ID, file_path.name))
            else:
                print("Telegram error: %s" % result, file=sys.stderr)
                sys.exit(1)
    except Exception as e:
        print("Failed to send file: %s" % e, file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: rex notify <message>")
        print("       rex notify --file <path> [caption]")
        sys.exit(1)

    if sys.argv[1] == "--file":
        if len(sys.argv) < 3:
            print("Usage: rex notify --file <path> [caption]", file=sys.stderr)
            sys.exit(1)
        caption = " ".join(sys.argv[3:]) if len(sys.argv) > 3 else None
        send_file(sys.argv[2], caption=caption)
    else:
        send(" ".join(sys.argv[1:]))
