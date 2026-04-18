"""OpenAI Whisper (STT) and TTS helpers for voice messages."""
from __future__ import annotations

import logging
from pathlib import Path

from openai import OpenAI

from claude_runner import CONFIG

logger = logging.getLogger(__name__)

STT_MODEL = CONFIG.get("whisper_model", "whisper-1")
TTS_MODEL = CONFIG.get("tts_model", "gpt-4o-mini-tts")
TTS_VOICE = CONFIG.get("tts_voice", "alloy")

# OpenAI TTS rejects inputs longer than 4096 chars.
TTS_MAX_CHARS = 4000


def _client() -> OpenAI:
    api_key = CONFIG.get("openai_api_key")
    if not api_key:
        raise RuntimeError(
            "openai_api_key is not configured. Run: rex config setup"
        )
    return OpenAI(api_key=api_key)


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
