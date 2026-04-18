"""OpenAI Whisper (STT) and TTS helpers for voice messages.

The `openai` package and the `openai_api_key` config entry are both
optional — voice features just become unavailable without them, while
the rest of rex keeps working. Use `unavailable_reason()` to render a
user-friendly message explaining why.
"""
from __future__ import annotations

import logging
from pathlib import Path

from claude_runner import CONFIG

logger = logging.getLogger(__name__)

STT_MODEL = CONFIG.get("whisper_model", "whisper-1")
TTS_MODEL = CONFIG.get("tts_model", "gpt-4o-mini-tts")
TTS_VOICE = CONFIG.get("tts_voice", "nova")

# OpenAI TTS rejects inputs longer than 4096 chars.
TTS_MAX_CHARS = 4000


class VoiceUnavailable(RuntimeError):
    """Raised when voice features can't run (missing package or API key)."""


def unavailable_reason() -> str | None:
    """Return a short human-readable reason voice is unavailable, or None."""
    try:
        import openai  # noqa: F401
    except ImportError:
        return "the `openai` Python package is not installed (run install.sh)"
    if not CONFIG.get("openai_api_key"):
        return "the OpenAI API key is not configured (run `rex config setup`)"
    return None


def _client():
    reason = unavailable_reason()
    if reason:
        raise VoiceUnavailable(reason)
    from openai import OpenAI
    return OpenAI(api_key=CONFIG["openai_api_key"])


def transcribe(path: Path) -> str:
    """Transcribe an audio file to text using Whisper."""
    with open(path, "rb") as fh:
        result = _client().audio.transcriptions.create(
            model=STT_MODEL,
            file=fh,
        )
    return (result.text or "").strip()


def synthesize(text: str, out_path: Path) -> Path:
    """Synthesize `text` to an OGG/Opus file suitable for Telegram voice."""
    clipped = text[:TTS_MAX_CHARS]
    if len(text) > TTS_MAX_CHARS:
        logger.info("TTS input clipped from %d to %d chars", len(text), TTS_MAX_CHARS)

    with _client().audio.speech.with_streaming_response.create(
        model=TTS_MODEL,
        voice=TTS_VOICE,
        input=clipped,
        response_format="opus",
    ) as response:
        response.stream_to_file(out_path)
    return out_path
