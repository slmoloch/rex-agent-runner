// Package voice wraps OpenAI's Whisper (STT) and TTS endpoints for Telegram
// voice messages. The feature is optional: if no API key is configured the
// constructor returns *Unavailable with a user-friendly reason.
package voice

import (
	"net/http"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/config"
)

const (
	defaultSTTModel = "whisper-1"
	defaultTTSModel = "gpt-4o-mini-tts"
	defaultTTSVoice = "nova"

	// TTSMaxChars matches OpenAI's 4096-char cap with a small safety margin.
	TTSMaxChars = 4000
)

// Unavailable is returned when voice can't run. Reason is safe to show users.
type Unavailable struct{ Reason string }

func (e *Unavailable) Error() string { return e.Reason }

// Client calls the two OpenAI endpoints rex needs. It is safe for concurrent
// use; the underlying http.Client is shared across goroutines.
type Client struct {
	apiKey   string
	http     *http.Client
	sttModel string
	ttsModel string
	ttsVoice string
}

// New builds a Client from config. Returns *Unavailable if voice can't run.
func New(cfg *config.Config) (*Client, error) {
	if cfg == nil || cfg.OpenAIAPIKey == "" {
		return nil, &Unavailable{Reason: "the OpenAI API key is not configured (run `rex config setup`)"}
	}
	return &Client{
		apiKey:   cfg.OpenAIAPIKey,
		http:     &http.Client{Timeout: 120 * time.Second},
		sttModel: firstNonEmpty(cfg.WhisperModel, defaultSTTModel),
		ttsModel: firstNonEmpty(cfg.TTSModel, defaultTTSModel),
		ttsVoice: firstNonEmpty(cfg.TTSVoice, defaultTTSVoice),
	}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
