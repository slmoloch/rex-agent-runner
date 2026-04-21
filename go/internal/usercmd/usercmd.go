// Package usercmd implements `rex user {text|voice|file}`. These commands are
// called by Claude during a turn to deliver messages to the user, and they
// record a marker file the daemon drains to know the reply was sent.
package usercmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/config"
	"github.com/slmoloch/rex-agent-runner/internal/telegram"
	"github.com/slmoloch/rex-agent-runner/internal/voice"
	"github.com/slmoloch/rex-agent-runner/internal/workspace"
)

const Usage = `Usage: rex user text <message>
       rex user voice <message>
       rex user file <path> [caption]`

// Run dispatches the user subcommand. Returns a non-nil error for failures;
// special ExitUnavailable is returned when voice is configured-off (exit 2).
func Run(ctx context.Context, cfg *config.Config, ws workspace.Paths, args []string) error {
	if len(args) == 0 {
		return errors.New(Usage)
	}
	tg, err := newTelegram(cfg)
	if err != nil {
		return err
	}
	switch args[0] {
	case "text":
		if len(args) < 2 {
			return errors.New("rex user text <message>")
		}
		msg := strings.Join(args[1:], " ")
		if err := tg.SendMessage(ctx, msg); err != nil {
			return err
		}
		return logSend(ws, "text", map[string]any{"message": msg})
	case "voice":
		if len(args) < 2 {
			return errors.New("rex user voice <message>")
		}
		msg := strings.TrimSpace(strings.Join(args[1:], " "))
		if msg == "" {
			return errors.New("voice message text is empty")
		}
		vc, err := voice.New(cfg)
		if err != nil {
			return &ExitUnavailable{Reason: err.Error()}
		}
		tmp, err := os.CreateTemp("", "rex-user-voice-*.ogg")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		_ = tmp.Close()
		defer os.Remove(tmpPath)

		if err := vc.Synthesize(ctx, msg, tmpPath); err != nil {
			return fmt.Errorf("TTS synthesis failed: %w", err)
		}
		if err := tg.SendVoice(ctx, tmpPath); err != nil {
			return err
		}
		return logSend(ws, "voice", map[string]any{"message": msg})
	case "file":
		if len(args) < 2 {
			return errors.New("rex user file <path> [caption]")
		}
		path := args[1]
		caption := strings.Join(args[2:], " ")
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("file not found: %s", path)
		}
		if err := tg.SendDocument(ctx, path, caption); err != nil {
			return err
		}
		payload := map[string]any{"path": path}
		if caption != "" {
			payload["caption"] = caption
		}
		return logSend(ws, "file", payload)
	default:
		return fmt.Errorf("unknown subcommand: %s\n%s", args[0], Usage)
	}
}

// ExitUnavailable signals an exit code of 2 — voice feature is off. The CLI
// layer maps it so agents can branch on it.
type ExitUnavailable struct{ Reason string }

func (e *ExitUnavailable) Error() string { return e.Reason }

func newTelegram(cfg *config.Config) (*telegram.Client, error) {
	if cfg.TelegramBotToken == "" {
		return nil, errors.New("telegram_bot_token not configured")
	}
	chat := cfg.TelegramChatID
	if chat == 0 && len(cfg.AllowedUserIDs) > 0 {
		chat = cfg.AllowedUserIDs[0]
	}
	if chat == 0 {
		return nil, errors.New("no telegram_chat_id and no allowed_user_ids")
	}
	return telegram.New(cfg.TelegramBotToken, chat), nil
}

// logSend appends one jsonl record to the current turn-marker file so the
// daemon knows this turn already delivered a reply.
func logSend(ws workspace.Paths, mode string, payload map[string]any) error {
	turnID := os.Getenv("REX_TURN_ID")
	if turnID == "" {
		return nil
	}
	if err := os.MkdirAll(ws.TurnMarkerDir, 0o755); err != nil {
		return nil // non-fatal
	}
	entry := map[string]any{"mode": mode, "ts": time.Now().Format(time.RFC3339Nano)}
	for k, v := range payload {
		entry[k] = v
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return nil
	}
	path := filepath.Join(ws.TurnMarkerDir, turnID+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
	return nil
}
