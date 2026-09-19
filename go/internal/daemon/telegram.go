package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
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

// origin is the conversation a Telegram message belongs to: the chat, the
// forum topic inside it (0 when there is none), the session target serving
// that conversation, and a Telegram client already addressed to it. Every
// forum topic gets its own session, so two topics in the same supergroup
// never share conversation history.
type origin struct {
	chatID   int64
	threadID int64
	target   string // "main" or "topic:<thread id>"
	tg       *telegram.Client
}

// originFor resolves the conversation a message belongs to and refreshes the
// topic binding on the way through, so out-of-band replies (callbacks,
// dispatches) can find their way back into the topic later.
func (d *Daemon) originFor(m *telegram.Message) origin {
	o := origin{chatID: m.Chat.ID, target: session.Main}
	if threadID := m.ThreadID(); threadID != 0 {
		o.threadID = threadID
		o.target = session.TopicTarget(threadID)
		d.sessions.TouchTopic(o.target, m.Chat.ID, threadID, m.TopicName())
	}
	o.tg = d.tg.WithChat(m.Chat.ID).WithThread(o.threadID)
	return o
}

func (d *Daemon) handleUpdate(ctx context.Context, u telegram.Update) {
	// Membership updates carry no message and are authorized on their own
	// actor, so they come before the message allow-list check.
	if u.MyChatMember != nil {
		d.handleMyChatMember(ctx, u.MyChatMember)
		return
	}
	if !d.authorized(u) {
		return
	}
	m := u.Message
	o := d.originFor(m)
	if m.IsForumService() {
		// Topic created / renamed / closed / reopened: originFor already
		// recorded whatever the service message told us. Nothing to answer.
		slog.Info("forum topic event", "chat", m.Chat.ID, "topic", o.target, "name", m.TopicName())
		return
	}
	if cmd := m.Command(); cmd != "" {
		switch cmd {
		case "start":
			_ = o.tg.SendMessage(ctx, d.startMessage(o))
		case "new":
			d.handleNewConversation(ctx, o)
		case "restart":
			_ = o.tg.SendMessage(ctx, "Restarting…")
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
		d.handleVoice(ctx, m, o)
		return
	}
	if attached := pickFile(m); attached.fileID != "" {
		d.handleFile(ctx, m, attached, o)
		return
	}
	if m.Text != "" {
		d.handleText(ctx, m, o)
	}
}

// startMessage tailors /start to where it was sent: inside a forum topic it
// says so, because the session the user is talking to is that topic's.
func (d *Daemon) startMessage(o origin) string {
	msg := "Hello! I'm a Claude Code bot. Send me a message and I'll process it through Claude Code.\n\n"
	if o.threadID != 0 {
		msg += "This topic has its own session — conversations in other topics stay separate.\n\n"
	}
	msg += "/new - Start a fresh conversation" + scopeSuffix(o) + "\n" +
		"/restart - Restart the bot daemon"
	return msg
}

// scopeSuffix labels a per-conversation action with the topic it applies to.
func scopeSuffix(o origin) string {
	if o.threadID == 0 {
		return ""
	}
	return " in this topic"
}

// handleNewConversation resets the session behind the conversation /new was
// sent in — the main session in a DM, or just that one topic's session in a
// forum. Other topics are untouched.
func (d *Daemon) handleNewConversation(ctx context.Context, o origin) {
	if d.sessions.GetID(o.target) != "" {
		func() {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			_, _ = d.runInSessionFrom(cctx, prepareResetPrompt, o.target, "reset-prepare", "", &o)
		}()
	}
	d.sessions.Reset(o.target)
	_ = o.tg.SendMessage(ctx, "Session reset"+scopeSuffix(o)+". Send a message to start fresh.")

	today := time.Now().Format("2006-01-02")
	initPrompt := "Your session has been reset (user requested via /new). Today's date is " + today + "."
	if o.threadID != 0 {
		initPrompt += " " + topicContextLine(d.topicLabel(o.target), o.threadID)
	}
	func() {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		_, _ = d.runInSessionFrom(cctx, initPrompt, o.target, "new-reset", "", &o)
	}()
}

// topicContextLine tells a freshly started session which forum topic it is
// serving, so the agent can scope its work (and its callbacks) accordingly.
func topicContextLine(name string, threadID int64) string {
	line := "You are running in a Telegram forum topic"
	if name != "" {
		line += " named " + strconv.Quote(name)
	}
	line += fmt.Sprintf(" (session target %s). This topic has its own session; "+
		"replies you send go back into it automatically.", session.TopicTarget(threadID))
	return line
}

// topicLabel is the human name of a topic when Telegram has told us one.
func (d *Daemon) topicLabel(target string) string {
	if t, ok := d.sessions.TopicFor(target); ok {
		return t.Name
	}
	return ""
}

func (d *Daemon) handleText(ctx context.Context, m *telegram.Message, o origin) {
	done := d.keepTyping(ctx, o.tg)
	defer done()

	resp, used := d.runInSessionFrom(ctx, d.withTopicContext(m.Text, o), o.target, "telegram", "", &o)
	if used {
		return
	}
	if resp == "" {
		slog.Warn("claude produced empty response; sending fallback", "trigger", "telegram")
		resp = emptyResponseFallback
	}
	if err := o.tg.SendMessageMarkdownChunks(ctx, resp); err != nil {
		slog.Error("send message failed", "err", err)
	}
}

// withTopicContext prefixes the first prompt of a topic session with a note
// naming the topic. Later turns resume the same session and don't need it.
func (d *Daemon) withTopicContext(prompt string, o origin) string {
	if o.threadID == 0 || d.sessions.GetID(o.target) != "" {
		return prompt
	}
	return topicContextLine(d.topicLabel(o.target), o.threadID) + "\n\n" + prompt
}

func (d *Daemon) handleVoice(ctx context.Context, m *telegram.Message, o origin) {
	if d.voice == nil {
		_ = o.tg.SendMessage(ctx,
			"I got your voice message, but voice support is off: the OpenAI API key is not configured.")
		return
	}
	if err := os.MkdirAll(d.ws.Inbox, 0o755); err != nil {
		slog.Error("inbox mkdir", "err", err)
		return
	}
	ts := time.Now().Format("20060102-150405")
	src := filepath.Join(d.ws.Inbox, fmt.Sprintf("%s-voice-%s.ogg", ts, m.Voice.FileUniqueID))

	tgFile, err := o.tg.GetFile(ctx, m.Voice.FileID)
	if err != nil {
		slog.Error("telegram getFile", "err", err)
		_ = o.tg.SendMessage(ctx, "Sorry, I couldn't download that voice message.")
		return
	}
	data, err := o.tg.Download(ctx, tgFile.FilePath)
	if err != nil {
		slog.Error("telegram download", "err", err)
		_ = o.tg.SendMessage(ctx, "Sorry, I couldn't download that voice message.")
		return
	}
	if err := os.WriteFile(src, data, 0o644); err != nil {
		slog.Error("save voice", "err", err)
		return
	}

	done := d.keepTyping(ctx, o.tg)
	defer done()

	transcript, err := d.voice.Transcribe(ctx, src)
	if err != nil {
		slog.Error("transcribe failed", "err", err)
		_ = o.tg.SendMessage(ctx, "Sorry, I couldn't understand that audio.")
		return
	}
	if transcript == "" {
		_ = o.tg.SendMessage(ctx, "Sorry, I couldn't understand that audio.")
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

	resp, used := d.runInSessionFrom(ctx, d.withTopicContext(prompt, o), o.target, "telegram-voice", "", &o)
	if used {
		return
	}
	if resp == "" {
		slog.Warn("claude produced empty response; sending fallback", "trigger", "telegram-voice")
		_ = o.tg.SendMessage(ctx, emptyResponseFallback)
		return
	}

	replyPath := filepath.Join(d.ws.Inbox, fmt.Sprintf("%s-reply-%s.ogg", ts, m.Voice.FileUniqueID))
	if err := d.voice.Synthesize(ctx, resp, replyPath); err != nil {
		slog.Error("synth failed, falling back to text", "err", err)
		_ = o.tg.SendMessageMarkdownChunks(ctx, resp)
		return
	}
	if err := o.tg.SendVoice(ctx, replyPath); err != nil {
		slog.Error("send voice failed", "err", err)
		_ = o.tg.SendMessageMarkdownChunks(ctx, resp)
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

func (d *Daemon) handleFile(ctx context.Context, m *telegram.Message, a attachedFile, o origin) {
	if err := os.MkdirAll(d.ws.Inbox, 0o755); err != nil {
		slog.Error("inbox mkdir", "err", err)
		return
	}
	tgFile, err := o.tg.GetFile(ctx, a.fileID)
	if err != nil {
		slog.Error("getFile", "err", err)
		_ = o.tg.SendMessage(ctx, "Sorry, I couldn't download that file.")
		return
	}
	data, err := o.tg.Download(ctx, tgFile.FilePath)
	if err != nil {
		slog.Error("download", "err", err)
		_ = o.tg.SendMessage(ctx, "Sorry, I couldn't download that file.")
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

	done := d.keepTyping(ctx, o.tg)
	defer done()

	resp, used := d.runInSessionFrom(ctx, d.withTopicContext(prompt, o), o.target, "telegram-file", "", &o)
	if used {
		return
	}
	if resp == "" {
		slog.Warn("claude produced empty response; sending fallback", "trigger", "telegram-file")
		resp = emptyResponseFallback
	}
	if err := o.tg.SendMessageMarkdownChunks(ctx, resp); err != nil {
		slog.Error("send message failed", "err", err)
	}
}

// keepTyping periodically sends the "typing" chat action to the chat (and
// forum topic) tg is addressed to, until the returned stop function is called.
func (d *Daemon) keepTyping(ctx context.Context, tg *telegram.Client) func() {
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		if err := tg.SendChatAction(ctx, "typing"); err != nil {
			return
		}
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_ = tg.SendChatAction(ctx, "typing")
			}
		}
	}()
	return func() { close(stop) }
}
