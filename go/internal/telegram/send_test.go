package telegram

import (
	"net/url"
	"testing"
)

func TestTargetIncludesThread(t *testing.T) {
	c := New("token", -100123).WithThread(77)
	v := url.Values{}
	c.target(v)
	if got := v.Get("chat_id"); got != "-100123" {
		t.Fatalf("chat_id = %q, want -100123", got)
	}
	if got := v.Get("message_thread_id"); got != "77" {
		t.Fatalf("message_thread_id = %q, want 77", got)
	}
	if got := c.targetFields()["message_thread_id"]; got != "77" {
		t.Fatalf("multipart message_thread_id = %q, want 77", got)
	}
}

func TestTargetOmitsThreadWhenUnbound(t *testing.T) {
	c := New("token", 5)
	v := url.Values{}
	c.target(v)
	if _, ok := v["message_thread_id"]; ok {
		t.Fatal("message_thread_id must be absent outside a forum topic")
	}
	if _, ok := c.targetFields()["message_thread_id"]; ok {
		t.Fatal("multipart message_thread_id must be absent outside a forum topic")
	}
}

func TestWithThreadAndWithChatLeaveOriginalAlone(t *testing.T) {
	base := New("token", 5)
	topic := base.WithChat(-100123).WithThread(77)
	if base.ChatID != 5 || base.ThreadID != 0 {
		t.Fatalf("base client mutated: chat=%d thread=%d", base.ChatID, base.ThreadID)
	}
	if topic.ChatID != -100123 || topic.ThreadID != 77 {
		t.Fatalf("derived client = chat %d thread %d, want -100123/77", topic.ChatID, topic.ThreadID)
	}
	if topic.HTTP != base.HTTP {
		t.Fatal("derived client should share the HTTP client")
	}
	// Switching chats drops a topic id that only means something in the old chat.
	if moved := topic.WithChat(-100456); moved.ThreadID != 0 {
		t.Fatalf("thread id survived a chat switch: %d", moved.ThreadID)
	}
}
