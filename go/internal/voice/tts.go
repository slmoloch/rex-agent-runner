package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
)

const ttsEndpoint = "https://api.openai.com/v1/audio/speech"

// Synthesize generates Opus audio for text and streams it to outPath. Text is
// clipped by rune count so we never split a multi-byte UTF-8 sequence.
func (c *Client) Synthesize(ctx context.Context, text, outPath string) error {
	runes := []rune(text)
	if len(runes) > TTSMaxChars {
		slog.Info("tts input clipped", "from", len(runes), "to", TTSMaxChars)
		text = string(runes[:TTSMaxChars])
	}

	payload, err := json.Marshal(map[string]string{
		"model":           c.ttsModel,
		"voice":           c.ttsVoice,
		"input":           text,
		"response_format": "opus",
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ttsEndpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tts request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return readAPIError("tts", resp)
	}

	// Stream straight to disk; the response can be several MB for long text.
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write opus: %w", err)
	}
	return nil
}
