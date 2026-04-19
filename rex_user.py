#!/usr/bin/env python3
"""Send a text, voice, or file message to the user via Telegram."""
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

WORKDIR = (BASE_DIR / CONFIG.get("workspace", "./workspace")).resolve()
REX_DIR = WORKDIR / ".rex"
MARKER_DIR = REX_DIR / "turn-markers"


def _log_send(mode, payload):
    """Record a successful send to this turn's marker file.

    The bot generates REX_TURN_ID per Claude invocation. When present, we
    append one JSON line per successful send so the bot can (a) know the
    agent already delivered its reply, and (b) log what was sent.
    """
    turn_id = os.environ.get("REX_TURN_ID")
    if not turn_id:
        return
    try:
        MARKER_DIR.mkdir(parents=True, exist_ok=True)
        entry = {"mode": mode}
        entry.update(payload)
        with open(MARKER_DIR / ("%s.jsonl" % turn_id), "a") as f:
            f.write(json.dumps(entry) + "\n")
    except Exception as e:
        # Never let marker I/O break the actual send.
        print("Warning: failed to write turn marker: %s" % e, file=sys.stderr)


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
                _log_send("text", {"message": message})
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
                payload = {"path": str(file_path)}
                if caption:
                    payload["caption"] = caption
                _log_send("file", payload)
                print("File sent to chat %s: %s" % (CHAT_ID, file_path.name))
            else:
                print("Telegram error: %s" % result, file=sys.stderr)
                sys.exit(1)
    except Exception as e:
        print("Failed to send file: %s" % e, file=sys.stderr)
        sys.exit(1)


def send_voice(text):
    """Synthesize `text` with OpenAI TTS and send as a Telegram voice message."""
    import tempfile
    import rex_whisper

    text = text.strip()
    if not text:
        print("Voice message text is empty.", file=sys.stderr)
        sys.exit(1)

    reason = rex_whisper.unavailable_reason()
    if reason:
        print(
            "Voice message not sent: %s.\n"
            "Fall back to `rex user text` to reach the user with plain text." % reason,
            file=sys.stderr,
        )
        sys.exit(2)

    with tempfile.NamedTemporaryFile(suffix=".ogg", delete=False) as tmp:
        tmp_path = Path(tmp.name)
    try:
        try:
            rex_whisper.synthesize(text, tmp_path)
        except rex_whisper.VoiceUnavailable as e:
            print(
                "Voice message not sent: %s.\n"
                "Fall back to `rex user text` to reach the user with plain text." % e,
                file=sys.stderr,
            )
            sys.exit(2)
        except Exception as e:
            print("TTS synthesis failed: %s" % e, file=sys.stderr)
            sys.exit(1)

        url = "https://api.telegram.org/bot%s/sendVoice" % TELEGRAM_TOKEN
        fields = {"chat_id": CHAT_ID}
        with open(tmp_path, "rb") as f:
            file_data = f.read()
        files = {"voice": ("voice.ogg", file_data, "audio/ogg")}
        body, multipart_ct = _build_multipart(fields, files)

        try:
            req = urllib.request.Request(url, data=body)
            req.add_header("Content-Type", multipart_ct)
            with urllib.request.urlopen(req, context=SSL_CONTEXT) as resp:
                result = json.loads(resp.read())
                if result.get("ok"):
                    _log_send("voice", {"message": text})
                    print("Voice message sent to chat %s" % CHAT_ID)
                else:
                    print("Telegram error: %s" % result, file=sys.stderr)
                    sys.exit(1)
        except Exception as e:
            print("Failed to send voice: %s" % e, file=sys.stderr)
            sys.exit(1)
    finally:
        tmp_path.unlink(missing_ok=True)


def _usage():
    print("Usage: rex user text <message>")
    print("       rex user voice <message>")
    print("       rex user file <path> [caption]")


if __name__ == "__main__":
    if len(sys.argv) < 2:
        _usage()
        sys.exit(1)

    sub = sys.argv[1]
    args = sys.argv[2:]

    if sub == "text":
        if not args:
            print("Usage: rex user text <message>", file=sys.stderr)
            sys.exit(1)
        send(" ".join(args))
    elif sub == "voice":
        if not args:
            print("Usage: rex user voice <message>", file=sys.stderr)
            sys.exit(1)
        send_voice(" ".join(args))
    elif sub == "file":
        if not args:
            print("Usage: rex user file <path> [caption]", file=sys.stderr)
            sys.exit(1)
        caption = " ".join(args[1:]) if len(args) > 1 else None
        send_file(args[0], caption=caption)
    elif sub in ("help", "-h", "--help"):
        _usage()
    else:
        print("Unknown subcommand: %s" % sub, file=sys.stderr)
        _usage()
        sys.exit(1)
