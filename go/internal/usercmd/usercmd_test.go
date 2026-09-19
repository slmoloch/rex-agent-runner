package usercmd

import (
	"testing"

	"github.com/slmoloch/rex-agent-runner/internal/config"
)

// A `rex user` call made inside a forum-topic turn must answer in that
// topic, not in the configured default chat.
func TestNewTelegramFollowsTurnTopic(t *testing.T) {
	t.Setenv("REX_TELEGRAM_CHAT_ID", "-100123")
	t.Setenv("REX_TELEGRAM_TOPIC_ID", "77")

	tg, err := newTelegram(&config.Config{TelegramBotToken: "token", TelegramChatID: 5})
	if err != nil {
		t.Fatalf("newTelegram: %v", err)
	}
	if tg.ChatID != -100123 || tg.ThreadID != 77 {
		t.Fatalf("client = chat %d thread %d, want -100123/77", tg.ChatID, tg.ThreadID)
	}
}

// Outside a topic turn — a DM, or a shell run by hand — nothing changes.
func TestNewTelegramDefaultsToConfiguredChat(t *testing.T) {
	t.Setenv("REX_TELEGRAM_CHAT_ID", "")
	t.Setenv("REX_TELEGRAM_TOPIC_ID", "")

	tg, err := newTelegram(&config.Config{TelegramBotToken: "token", TelegramChatID: 5})
	if err != nil {
		t.Fatalf("newTelegram: %v", err)
	}
	if tg.ChatID != 5 || tg.ThreadID != 0 {
		t.Fatalf("client = chat %d thread %d, want 5/0", tg.ChatID, tg.ThreadID)
	}

	tg, err = newTelegram(&config.Config{TelegramBotToken: "token", AllowedUserIDs: []int64{42}})
	if err != nil {
		t.Fatalf("newTelegram: %v", err)
	}
	if tg.ChatID != 42 {
		t.Fatalf("client chat = %d, want the first allowed user id", tg.ChatID)
	}
}
