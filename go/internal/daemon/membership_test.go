package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slmoloch/rex-agent-runner/internal/config"
	"github.com/slmoloch/rex-agent-runner/internal/telegram"
)

// newJoinDaemon builds a daemon whose config lives in a temp project dir,
// seeded with the given config.json body.
func newJoinDaemon(t *testing.T, configJSON string) (*Daemon, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return &Daemon{
		cfg:     cfg,
		tg:      telegram.New("token", cfg.TelegramChatID),
		allowed: map[int64]struct{}{5: {}},
	}, dir
}

func configuredChat(t *testing.T, dir string) int64 {
	t.Helper()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	return cfg.TelegramChatID
}

func TestAdoptDefaultChatWhenUnset(t *testing.T) {
	d, dir := newJoinDaemon(t, `{"telegram_bot_token":"token","allowed_user_ids":[5]}`)

	adopted, current, err := d.adoptDefaultChat(-100123)
	if err != nil {
		t.Fatalf("adoptDefaultChat: %v", err)
	}
	if !adopted || current != -100123 {
		t.Fatalf("adopted=%v current=%d, want true/-100123", adopted, current)
	}
	if got := configuredChat(t, dir); got != -100123 {
		t.Fatalf("telegram_chat_id on disk = %d, want -100123", got)
	}
}

// An existing default chat is never silently replaced: the agent's messages
// would start going somewhere else.
func TestAdoptDefaultChatKeepsExisting(t *testing.T) {
	d, dir := newJoinDaemon(t, `{"telegram_bot_token":"token","allowed_user_ids":[5],"telegram_chat_id":5}`)

	adopted, current, err := d.adoptDefaultChat(-100123)
	if err != nil {
		t.Fatalf("adoptDefaultChat: %v", err)
	}
	if adopted || current != 5 {
		t.Fatalf("adopted=%v current=%d, want false/5", adopted, current)
	}
	if got := configuredChat(t, dir); got != 5 {
		t.Fatalf("telegram_chat_id on disk = %d, want it untouched at 5", got)
	}
}

// Adoption must not clobber the rest of config.json, including a value
// written while the daemon was running.
func TestAdoptDefaultChatRereadsConfig(t *testing.T) {
	d, dir := newJoinDaemon(t, `{"telegram_bot_token":"token","allowed_user_ids":[5],"openai_api_key":"sk-test"}`)

	// Someone edits the file after the daemon loaded it.
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"telegram_bot_token":"token","allowed_user_ids":[5,9],"openai_api_key":"sk-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := d.adoptDefaultChat(-100123); err != nil {
		t.Fatalf("adoptDefaultChat: %v", err)
	}

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramChatID != -100123 {
		t.Fatalf("chat id = %d, want -100123", cfg.TelegramChatID)
	}
	if cfg.OpenAIAPIKey != "sk-test" || len(cfg.AllowedUserIDs) != 2 {
		t.Fatalf("adoption clobbered the config: %+v", cfg)
	}
}

// Anyone can drop a bot into a group; only the allow-list may bind rex to
// one. An unauthorized add must leave the config alone.
func TestUnauthorizedAddDoesNotRegister(t *testing.T) {
	d, dir := newJoinDaemon(t, `{"telegram_bot_token":"token","allowed_user_ids":[5]}`)

	d.handleMyChatMember(context.Background(), &telegram.ChatMemberUpdated{
		Chat:          telegram.Chat{ID: -100999, Type: "supergroup", IsForum: true},
		From:          &telegram.User{ID: 777},
		NewChatMember: telegram.ChatMember{Status: "member"},
	})

	if got := configuredChat(t, dir); got != 0 {
		t.Fatalf("telegram_chat_id = %d, want it unset after an unauthorized add", got)
	}
}

// Being removed from a chat, or added to a DM/channel, is not a join.
func TestNonJoinEventsAreIgnored(t *testing.T) {
	d, dir := newJoinDaemon(t, `{"telegram_bot_token":"token","allowed_user_ids":[5]}`)
	ctx := context.Background()

	d.handleMyChatMember(ctx, &telegram.ChatMemberUpdated{
		Chat:          telegram.Chat{ID: -100999, Type: "supergroup"},
		From:          &telegram.User{ID: 5},
		NewChatMember: telegram.ChatMember{Status: "kicked"},
	})
	d.handleMyChatMember(ctx, &telegram.ChatMemberUpdated{
		Chat:          telegram.Chat{ID: 5, Type: "private"},
		From:          &telegram.User{ID: 5},
		NewChatMember: telegram.ChatMember{Status: "member"},
	})

	if got := configuredChat(t, dir); got != 0 {
		t.Fatalf("telegram_chat_id = %d, want it unset", got)
	}
}

func TestJoinMessage(t *testing.T) {
	forum := telegram.Chat{ID: -100123, Type: "supergroup", IsForum: true}

	adopted := joinMessage(forum, true, -100123)
	if !strings.Contains(adopted, "Registered this chat (id -100123)") {
		t.Fatalf("adoption message = %q", adopted)
	}
	if !strings.Contains(adopted, "every topic gets its own session") {
		t.Fatalf("forum message should explain per-topic sessions: %q", adopted)
	}

	other := joinMessage(forum, false, 5)
	if !strings.Contains(other, "My default chat is 5") {
		t.Fatalf("message should name the configured chat: %q", other)
	}

	plain := joinMessage(telegram.Chat{ID: -100123, Type: "group"}, true, -100123)
	if !strings.Contains(plain, "Turn on Topics") {
		t.Fatalf("non-forum message should suggest topics: %q", plain)
	}

	// Every variant mentions the privacy setting rex cannot change itself.
	for _, msg := range []string{adopted, other, plain} {
		if !strings.Contains(msg, "Group Privacy") {
			t.Fatalf("message should mention group privacy: %q", msg)
		}
	}
}
