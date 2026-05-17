// Package usercmd implements `rex user {text|rich-text|voice|file}`. These
// commands are called by Claude during a turn to deliver messages to the user.
package usercmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/slmoloch/rex-agent-runner/internal/config"
	"github.com/slmoloch/rex-agent-runner/internal/telegram"
	"github.com/slmoloch/rex-agent-runner/internal/voice"
	"github.com/slmoloch/rex-agent-runner/internal/workspace"
)

const Usage = `Usage: rex user text <message>            inline message with Telegram Markdown (V1) formatting
       rex user text --file <path>        read the Markdown V1 message body from a file
       rex user rich-text <message>       inline message with Telegram HTML formatting
       rex user rich-text --file <path>   read the HTML message body from a file
       rex user voice <message>           text-to-speech voice note
       rex user file <path> [caption]     upload the file as a Telegram attachment

Notes:
  - 'text' interprets the message as Telegram Markdown V1 (*bold*, _italic_, ` + "`code`" + `, [link](url)).
  - 'rich-text' interprets the message as Telegram HTML (<b>, <i>, <code>, <a>, ...).
  - Use '--file <path>' with 'text' or 'rich-text' to send the file's contents as
    the message body — useful for long or multi-line messages where shell quoting
    is awkward.
  - 'file' sends the file as a downloadable attachment only — its contents are NOT
    shown inline. To deliver text content inline, use '--file' with 'user text' or
    'user rich-text'.`

// Run dispatches the user subcommand. Returns a non-nil error for failures;
// special ExitUnavailable is returned when voice is configured-off (exit 2).
func Run(ctx context.Context, cfg *config.Config, _ workspace.Paths, args []string) error {
	if len(args) == 0 {
		return errors.New(Usage)
	}
	tg, err := newTelegram(cfg)
	if err != nil {
		return err
	}
	switch args[0] {
	case "text":
		msg, err := messageBody(args[1:], "rex user text <message> | rex user text --file <path>")
		if err != nil {
			return err
		}
		return tg.SendMessageMarkdown(ctx, msg)
	case "rich-text":
		msg, err := messageBody(args[1:], "rex user rich-text <message> | rex user rich-text --file <path>")
		if err != nil {
			return err
		}
		return tg.SendMessageHTML(ctx, msg)
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
		return tg.SendVoice(ctx, tmpPath)
	case "file":
		if len(args) < 2 {
			return errors.New("rex user file <path> [caption]")
		}
		path := args[1]
		caption := strings.Join(args[2:], " ")
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("file not found: %s", path)
		}
		return tg.SendDocument(ctx, path, caption)
	default:
		return fmt.Errorf("unknown subcommand: %s\n%s", args[0], Usage)
	}
}

// messageBody resolves the message body for `text` / `rich-text` from either
// positional args or `--file <path>`. The flag must be the first arg and
// consumes its value; mixing positional text with --file is rejected.
func messageBody(args []string, usage string) (string, error) {
	if len(args) == 0 {
		return "", errors.New(usage)
	}
	if args[0] == "--file" {
		if len(args) < 2 {
			return "", errors.New("--file requires a path")
		}
		if len(args) > 2 {
			return "", errors.New("--file takes a single path and no other arguments")
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			return "", fmt.Errorf("read --file: %w", err)
		}
		msg := strings.TrimRight(string(data), "\n")
		if msg == "" {
			return "", fmt.Errorf("file is empty: %s", args[1])
		}
		return msg, nil
	}
	return strings.Join(args, " "), nil
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
