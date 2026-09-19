package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/slmoloch/rex-agent-runner/internal/config"
	"github.com/slmoloch/rex-agent-runner/internal/telegram"
)

// handleMyChatMember reacts to the bot being added to (or removed from) a
// chat. Telegram delivers these updates whatever the group privacy setting
// is, so this is the one moment rex can introduce itself reliably — and, if
// no default chat has been configured yet, adopt this one without the user
// having to dig the id out of getUpdates by hand.
func (d *Daemon) handleMyChatMember(ctx context.Context, ev *telegram.ChatMemberUpdated) {
	var actor int64
	if ev.From != nil {
		actor = ev.From.ID
	}
	slog.Info("bot membership changed",
		"chat", ev.Chat.ID, "type", ev.Chat.Type, "forum", ev.Chat.IsForum,
		"status", ev.NewChatMember.Status, "by", actor)

	if !ev.NewChatMember.Joined() || !ev.Chat.IsGroup() {
		return
	}
	// Only the allow-list may bind rex to a chat: anyone can drop a bot into
	// a group, and adopting that chat would redirect the agent's messages.
	if _, ok := d.allowed[actor]; !ok {
		slog.Warn("ignoring group add by unauthorized user", "chat", ev.Chat.ID, "from", actor)
		return
	}

	tg := d.tg.WithChat(ev.Chat.ID)
	adopted, current, err := d.adoptDefaultChat(ev.Chat.ID)
	if err != nil {
		slog.Error("could not record chat id", "chat", ev.Chat.ID, "err", err)
		_ = tg.SendMessage(ctx, fmt.Sprintf(
			"I couldn't save this chat's id (%d) to my config: %v\nSet telegram_chat_id by hand with `rex config setup`.",
			ev.Chat.ID, err))
		return
	}
	_ = tg.SendMessage(ctx, joinMessage(ev.Chat, adopted, current))
}

// adoptDefaultChat records chatID as telegram_chat_id when none is set yet.
// It reloads config.json first so a value written while the daemon was
// running is not clobbered, and reports the configured chat either way.
// `rex user` reads the config on every call, so the change takes effect
// without a restart.
func (d *Daemon) adoptDefaultChat(chatID int64) (adopted bool, current int64, err error) {
	cfg, err := config.Load(d.cfg.ProjectDir())
	if err != nil {
		return false, 0, err
	}
	if cfg.TelegramChatID != 0 {
		return false, cfg.TelegramChatID, nil
	}
	cfg.TelegramChatID = chatID
	if err := cfg.Save(); err != nil {
		return false, 0, err
	}
	slog.Info("adopted chat as telegram_chat_id", "chat", chatID)
	return true, chatID, nil
}

// joinMessage is what rex says when it lands in a group: what it just did
// with the chat id, how topics map to sessions, and the one setting that
// still has to be changed in BotFather.
func joinMessage(chat telegram.Chat, adopted bool, current int64) string {
	msg := "Hi! I'm rex, a Claude Code agent.\n\n"
	switch {
	case adopted:
		msg += fmt.Sprintf("Registered this chat (id %d) as my default — no config needed.\n", chat.ID)
	case current == chat.ID:
		msg += fmt.Sprintf("This chat (id %d) is already my default.\n", chat.ID)
	default:
		msg += fmt.Sprintf("This chat's id is %d. My default chat is %d — "+
			"run `rex config setup` if you want to switch.\n", chat.ID, current)
	}
	if chat.IsForum {
		msg += "\nTopics are on here, so every topic gets its own session: " +
			"separate history, replies in the topic they were asked in. " +
			"Create a topic and say hi.\n"
	} else {
		msg += "\nTurn on Topics for this group and every topic gets its own session.\n"
	}
	msg += "\nIf I stay silent when you write, group privacy is still on: " +
		"in BotFather, Bot Settings → Group Privacy → Turn off, then remove and re-add me."
	return msg
}
