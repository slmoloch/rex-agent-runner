package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Update is a minimal subset of Telegram's Update object. We decode only what
// the bot acts on.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text,omitempty"`
	Caption   string `json:"caption,omitempty"`

	Voice    *Voice    `json:"voice,omitempty"`
	Document *Document `json:"document,omitempty"`
	Photo    []Photo   `json:"photo,omitempty"`
	Audio    *Audio    `json:"audio,omitempty"`
	Video    *Video    `json:"video,omitempty"`

	// Command is populated by a convenience helper when Text starts with "/".
	command string `json:"-"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
}

type Photo struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

type Audio struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
}

type Video struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
}

// Command returns "start" for "/start foo", "" for non-command text.
func (m *Message) Command() string {
	if m.command != "" {
		return m.command
	}
	if !strings.HasPrefix(m.Text, "/") {
		return ""
	}
	rest := m.Text[1:]
	// Strip @botusername suffix: "/start@mybot" -> "start"
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		rest = rest[:at]
	}
	// Drop any arguments after the first space.
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp]
	}
	m.command = rest
	return rest
}

// GetUpdates long-polls for new updates. Returns the next offset the caller
// should pass on the next call.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, int64, error) {
	v := url.Values{}
	if offset > 0 {
		v.Set("offset", strconv.FormatInt(offset, 10))
	}
	v.Set("timeout", strconv.Itoa(timeoutSec))
	// Accept every update type; we filter in the handler.
	raw, err := c.doJSON(ctx, "getUpdates", v)
	if err != nil {
		return nil, offset, err
	}
	var updates []Update
	if err := jsonUnmarshal(raw, &updates); err != nil {
		return nil, offset, fmt.Errorf("getUpdates decode: %w", err)
	}
	next := offset
	for _, u := range updates {
		if u.UpdateID >= next {
			next = u.UpdateID + 1
		}
	}
	return updates, next, nil
}

// jsonUnmarshal is a tiny indirection so send.go / updates.go can decode
// without each file re-importing encoding/json.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
