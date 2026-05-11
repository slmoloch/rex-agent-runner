package telegram

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SendMessage posts a plain text message to the configured chat. No
// parse_mode is set, so the body is delivered verbatim with no markup
// interpreted. Use SendMessageHTML when you want Telegram HTML rendering.
func (c *Client) SendMessage(ctx context.Context, text string) error {
	v := url.Values{}
	v.Set("chat_id", c.strChatID())
	v.Set("text", text)
	_, err := c.doJSON(ctx, "sendMessage", v)
	return err
}

// SendMessageMarkdown posts a message with parse_mode=Markdown (Telegram's
// legacy Markdown V1). If Telegram rejects the markup, retry once as plain
// text so the user still receives the content instead of a hard error.
func (c *Client) SendMessageMarkdown(ctx context.Context, text string) error {
	v := url.Values{}
	v.Set("chat_id", c.strChatID())
	v.Set("text", text)
	v.Set("parse_mode", "Markdown")
	_, err := c.doJSON(ctx, "sendMessage", v)
	if err != nil && isParseError(err) {
		slog.Warn("telegram markdown rejected; resending as plain text", "err", err)
		v.Del("parse_mode")
		_, err = c.doJSON(ctx, "sendMessage", v)
	}
	return err
}

// SendMessageHTML posts a message with parse_mode=HTML. If Telegram rejects
// the markup, retry once as plain text (with tags stripped and entities
// unescaped) so the user still receives readable content instead of a hard
// error.
func (c *Client) SendMessageHTML(ctx context.Context, text string) error {
	v := url.Values{}
	v.Set("chat_id", c.strChatID())
	v.Set("text", text)
	v.Set("parse_mode", "HTML")
	_, err := c.doJSON(ctx, "sendMessage", v)
	if err != nil && isParseError(err) {
		slog.Warn("telegram html rejected; resending as plain text", "err", err)
		v.Del("parse_mode")
		v.Set("text", stripHTMLFormatting(text))
		_, err = c.doJSON(ctx, "sendMessage", v)
	}
	return err
}

// isParseError returns true for Telegram errors caused by malformed
// parse_mode markup, which we recover from by resending without parse_mode.
func isParseError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "can't parse entities") ||
		strings.Contains(s, "can't find end") ||
		strings.Contains(s, "unsupported start tag") ||
		strings.Contains(s, "unallowed start tag") ||
		strings.Contains(s, "expected end tag")
}

// htmlTagRE matches anything that looks like an HTML tag (opening, closing,
// or with attributes). Used to scrub tags out of the plain-text fallback.
var htmlTagRE = regexp.MustCompile(`<[^>]+>`)

// stripHTMLFormatting removes HTML tags and unescapes &lt; &gt; &amp; so
// the plain-text fallback doesn't surface raw markup the user wasn't meant
// to see.
func stripHTMLFormatting(s string) string {
	s = htmlTagRE.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

// SendMessageChunks splits text at 4096 chars (Telegram's hard limit) and
// sends each piece sequentially. Mirrors rex_bot.py's _send_response.
func (c *Client) SendMessageChunks(ctx context.Context, text string) error {
	const max = 4096
	if len(text) <= max {
		return c.SendMessage(ctx, text)
	}
	for i := 0; i < len(text); i += max {
		end := i + max
		if end > len(text) {
			end = len(text)
		}
		if err := c.SendMessage(ctx, text[i:end]); err != nil {
			return err
		}
	}
	return nil
}

// SendVoice sends an opus/ogg voice message from disk.
func (c *Client) SendVoice(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fields := map[string]string{"chat_id": c.strChatID()}
	files := map[string]filePart{
		"voice": {name: "voice.ogg", data: data},
	}
	_, err = c.doMultipart(ctx, "sendVoice", fields, files)
	return err
}

// SendDocument uploads a file to the configured chat with an optional caption.
func (c *Client) SendDocument(ctx context.Context, path, caption string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fields := map[string]string{"chat_id": c.strChatID()}
	if caption != "" {
		fields["caption"] = caption
		fields["parse_mode"] = "HTML"
	}
	ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	_ = ctype // Telegram detects type; we keep the hint ready if needed later.
	files := map[string]filePart{
		"document": {name: filepath.Base(path), data: data},
	}
	_, err = c.doMultipart(ctx, "sendDocument", fields, files)
	if err != nil && caption != "" && isParseError(err) {
		slog.Warn("telegram caption html rejected; resending as plain text", "err", err)
		delete(fields, "parse_mode")
		fields["caption"] = stripHTMLFormatting(caption)
		// Multipart bodies are single-use; reattach the same payload.
		files = map[string]filePart{
			"document": {name: filepath.Base(path), data: data},
		}
		_, err = c.doMultipart(ctx, "sendDocument", fields, files)
	}
	return err
}

// SendChatAction signals "typing" (or other action) in the chat.
func (c *Client) SendChatAction(ctx context.Context, action string) error {
	v := url.Values{}
	v.Set("chat_id", c.strChatID())
	v.Set("action", action)
	_, err := c.doJSON(ctx, "sendChatAction", v)
	return err
}

// GetFile resolves a file_id to its file_path, which can then be downloaded.
type File struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	FileSize int64  `json:"file_size"`
}

func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	v := url.Values{}
	v.Set("file_id", fileID)
	raw, err := c.doJSON(ctx, "getFile", v)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := jsonUnmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("getFile decode: %w", err)
	}
	return f, nil
}
