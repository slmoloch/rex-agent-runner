package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/session"
	"github.com/slmoloch/rex-agent-runner/internal/telegram"
)

const longPollTimeout = 30

// Sent when a turn ends without a rex user reply or any Claude text — prevents
// silent drops on timeout / failure / empty-result paths.
const emptyResponseFallback = "Sorry, I couldn't produce a response. Please try again."

// pollTelegram runs the long-polling loop until ctx is cancelled.
func (d *Daemon) pollTelegram(ctx context.Context) error {
	slog.Info("telegram bot started")
	var offset int64
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pctx, cancel := context.WithTimeout(ctx, (longPollTimeout+10)*time.Second)
		updates, next, err := d.tg.GetUpdates(pctx, offset, longPollTimeout)
		cancel()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			slog.Warn("getUpdates failed", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		offset = next
		for _, u := range updates {
			d.handleUpdate(ctx, u)
		}
	}
}

func (d *Daemon) authorized(u telegram.Update) bool {
	if u.Message == nil || u.Message.From == nil {
		return false
	}
	_, ok := d.allowed[u.Message.From.ID]
	if !ok {
		slog.Warn("unauthorized update", "from", u.Message.From.ID)
	}
	return ok
}

func (d *Daemon) handleUpdate(ctx context.Context, u telegram.Update) {
	if !d.authorized(u) {
		return
	}
	m := u.Message
	if cmd := m.Command(); cmd != "" {
		switch cmd {
		case "start":
			_ = d.tg.SendMessage(ctx,
				"Hello! I'm a Claude Code bot. Send me a message and I'll process it through Claude Code.\n\n"+
					"/new - Start a fresh conversation\n"+
					"/restart - Restart the bot daemon")
		case "new":
			d.handleNewConversation(ctx)
		case "restart":
			_ = d.tg.SendMessage(ctx, "Restarting…")
			slog.Info("restart requested via telegram", "user", m.From.ID)
			// Exit so the process manager (launchd / systemd) relaunches us.
			go func() {
				time.Sleep(time.Second)
				os.Exit(0)
			}()
		}
		return
	}
	if m.Voice != nil {
		d.handleVoice(ctx, m)
		return
	}
	if attached := pickFile(m); attached.fileID != "" {
		d.handleFile(ctx, m, attached)
		return
	}
	if m.Text != "" {
		d.handleText(ctx, m)
	}
}

func (d *Daemon) handleNewConversation(ctx context.Context) {
	mainID := d.sessions.GetMainID()
	if mainID != "" {
		func() {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			_, _ = d.runInSession(cctx, prepareResetPrompt, session.Main, "reset-prepare", "")
		}()
	}
	d.sessions.ResetMain()
	_ = d.tg.SendMessage(ctx, "Session reset. Send a message to start fresh.")
}

func (d *Daemon) handleText(ctx context.Context, m *telegram.Message) {
	done := d.keepTyping(ctx)
	defer done()

	resp, used := d.runInSession(ctx, m.Text, session.Main, "telegram", "")
	if used {
		return
	}
	if resp == "" {
		slog.Warn("claude produced empty response; sending fallback", "trigger", "telegram")
		resp = emptyResponseFallback
	}
	if err := d.tg.SendMessageChunks(ctx, resp); err != nil {
		slog.Error("send message failed", "err", err)
	}
}

func (d *Daemon) handleVoice(ctx context.Context, m *telegram.Message) {
	if d.voice == nil {
		_ = d.tg.SendMessage(ctx,
			"I got your voice message, but voice support is off: the OpenAI API key is not configured.")
		return
	}
	if err := os.MkdirAll(d.ws.Inbox, 0o755); err != nil {
		slog.Error("inbox mkdir", "err", err)
		return
	}
	ts := time.Now().Format("20060102-150405")
	src := filepath.Join(d.ws.Inbox, fmt.Sprintf("%s-voice-%s.ogg", ts, m.Voice.FileUniqueID))

	tgFile, err := d.tg.GetFile(ctx, m.Voice.FileID)
	if err != nil {
		slog.Error("telegram getFile", "err", err)
		_ = d.tg.SendMessage(ctx, "Sorry, I couldn't download that voice message.")
		return
	}
	data, err := d.tg.Download(ctx, tgFile.FilePath)
	if err != nil {
		slog.Error("telegram download", "err", err)
		_ = d.tg.SendMessage(ctx, "Sorry, I couldn't download that voice message.")
		return
	}
	if err := os.WriteFile(src, data, 0o644); err != nil {
		slog.Error("save voice", "err", err)
		return
	}

	done := d.keepTyping(ctx)
	defer done()

	transcript, err := d.voice.Transcribe(ctx, src)
	if err != nil {
		slog.Error("transcribe failed", "err", err)
		_ = d.tg.SendMessage(ctx, "Sorry, I couldn't understand that audio.")
		return
	}
	if transcript == "" {
		_ = d.tg.SendMessage(ctx, "Sorry, I couldn't understand that audio.")
		return
	}
	slog.Info("voice transcript", "preview", truncate(transcript, 300))

	caption := strings.TrimSpace(m.Caption)
	prompt := "The user sent a Telegram voice message, transcribed below via Whisper.\n" +
		"Reply conversationally — your response will be synthesized back to audio and sent as " +
		"a voice reply, so keep it concise and suitable for speech (no markdown, code blocks, " +
		"or long lists).\n\nTranscript: " + transcript
	if caption != "" {
		prompt += "\nCaption: " + caption
	}

	resp, used := d.runInSession(ctx, prompt, session.Main, "telegram-voice", "")
	if used {
		return
	}
	if resp == "" {
		slog.Warn("claude produced empty response; sending fallback", "trigger", "telegram-voice")
		_ = d.tg.SendMessage(ctx, emptyResponseFallback)
		return
	}

	replyPath := filepath.Join(d.ws.Inbox, fmt.Sprintf("%s-reply-%s.ogg", ts, m.Voice.FileUniqueID))
	if err := d.voice.Synthesize(ctx, resp, replyPath); err != nil {
		slog.Error("synth failed, falling back to text", "err", err)
		_ = d.tg.SendMessageChunks(ctx, resp)
		return
	}
	if err := d.tg.SendVoice(ctx, replyPath); err != nil {
		slog.Error("send voice failed", "err", err)
		_ = d.tg.SendMessageChunks(ctx, resp)
	}
}

type attachedFile struct {
	fileID   string
	name     string
	fileKind string // "document" | "photo" | "voice" | "audio" | "video" | ...
}

func pickFile(m *telegram.Message) attachedFile {
	switch {
	case m.Document != nil:
		name := m.Document.FileName
		if name == "" {
			name = "document-" + m.Document.FileUniqueID
		}
		return attachedFile{fileID: m.Document.FileID, name: name, fileKind: "document"}
	case len(m.Photo) > 0:
		p := m.Photo[len(m.Photo)-1]
		return attachedFile{fileID: p.FileID, name: "photo-" + p.FileUniqueID + ".jpg", fileKind: "photo"}
	case m.Audio != nil:
		name := m.Audio.FileName
		if name == "" {
			name = "audio-" + m.Audio.FileUniqueID + ".mp3"
		}
		return attachedFile{fileID: m.Audio.FileID, name: name, fileKind: "audio"}
	case m.Video != nil:
		name := m.Video.FileName
		if name == "" {
			name = "video-" + m.Video.FileUniqueID + ".mp4"
		}
		return attachedFile{fileID: m.Video.FileID, name: name, fileKind: "video"}
	}
	return attachedFile{}
}

func (d *Daemon) handleFile(ctx context.Context, m *telegram.Message, a attachedFile) {
	if err := os.MkdirAll(d.ws.Inbox, 0o755); err != nil {
		slog.Error("inbox mkdir", "err", err)
		return
	}
	tgFile, err := d.tg.GetFile(ctx, a.fileID)
	if err != nil {
		slog.Error("getFile", "err", err)
		_ = d.tg.SendMessage(ctx, "Sorry, I couldn't download that file.")
		return
	}
	data, err := d.tg.Download(ctx, tgFile.FilePath)
	if err != nil {
		slog.Error("download", "err", err)
		_ = d.tg.SendMessage(ctx, "Sorry, I couldn't download that file.")
		return
	}
	ts := time.Now().Format("20060102-150405")
	dest := filepath.Join(d.ws.Inbox, ts+"-"+filepath.Base(a.name))
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		slog.Error("save file", "err", err)
		return
	}

	rel, err := filepath.Rel(d.ws.Root, dest)
	if err != nil {
		rel = dest
	}
	caption := strings.TrimSpace(m.Caption)
	lines := []string{
		"The user sent a file via Telegram.",
		"Path (relative to workspace): " + rel,
		fmt.Sprintf("Size: %d bytes", len(data)),
	}
	if caption != "" {
		lines = append(lines, "Caption: "+caption)
	} else {
		lines = append(lines,
			"No caption was provided. Inspect the file if appropriate, then acknowledge receipt and ask what to do with it.")
	}
	prompt := strings.Join(lines, "\n")

	done := d.keepTyping(ctx)
	defer done()

	resp, used := d.runInSession(ctx, prompt, session.Main, "telegram-file", "")
	if used {
		return
	}
	if resp == "" {
		slog.Warn("claude produced empty response; sending fallback", "trigger", "telegram-file")
		resp = emptyResponseFallback
	}
	if err := d.tg.SendMessageChunks(ctx, resp); err != nil {
		slog.Error("send message failed", "err", err)
	}
}

// keepTyping periodically sends the "typing" chat action until the returned
// stop function is called.
func (d *Daemon) keepTyping(ctx context.Context) func() {
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		if err := d.tg.SendChatAction(ctx, "typing"); err != nil {
			return
		}
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_ = d.tg.SendChatAction(ctx, "typing")
			}
		}
	}()
	return func() { close(stop) }
}
