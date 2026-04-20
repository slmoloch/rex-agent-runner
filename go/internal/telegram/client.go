// Package telegram is a stdlib-only Bot API client. It covers exactly the
// endpoints rex needs: sendMessage, sendVoice, sendDocument, sendChatAction,
// getUpdates (long polling), getFile + file download.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
)

const apiRoot = "https://api.telegram.org"

type Client struct {
	Token  string
	ChatID int64
	HTTP   *http.Client
}

func New(token string, chatID int64) *Client {
	return &Client{
		Token:  token,
		ChatID: chatID,
		HTTP:   &http.Client{Timeout: 0}, // per-call ctx handles timeouts
	}
}

func (c *Client) endpoint(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", apiRoot, c.Token, method)
}

// fileURL returns the URL for downloading a file given its Telegram file_path.
func (c *Client) fileURL(filePath string) string {
	return fmt.Sprintf("%s/file/bot%s/%s", apiRoot, c.Token, filePath)
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

func (c *Client) doJSON(ctx context.Context, method string, payload url.Values) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method),
		bytes.NewBufferString(payload.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req)
}

func (c *Client) doMultipart(ctx context.Context, method string, fields map[string]string, files map[string]filePart) (json.RawMessage, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	for k, fp := range files {
		fw, err := mw.CreateFormFile(k, fp.name)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(fp.data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.do(req)
}

func (c *Client) do(req *http.Request) (json.RawMessage, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r apiResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("telegram: decode: %w: %s", err, data)
	}
	if !r.OK {
		return nil, fmt.Errorf("telegram: %s", r.Description)
	}
	return r.Result, nil
}

type filePart struct {
	name string
	data []byte
}

// strChatID formats ChatID for form encoding.
func (c *Client) strChatID() string { return strconv.FormatInt(c.ChatID, 10) }

// Download fetches a file by Telegram file_path and returns its bytes.
func (c *Client) Download(ctx context.Context, filePath string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fileURL(filePath), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("telegram file GET %s: %s", filePath, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Timestamp is the type used by Update.Date. Declared here so other packages
// can use it without importing this whole file.
type Timestamp = int64
