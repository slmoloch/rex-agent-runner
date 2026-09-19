package telegram

import (
	"encoding/json"
	"testing"
)

// decodeMessage decodes a raw Telegram update payload the way GetUpdates does.
func decodeMessage(t *testing.T, raw string) *Message {
	t.Helper()
	var u Update
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if u.Message == nil {
		t.Fatal("update carried no message")
	}
	return u.Message
}

func TestThreadIDForForumTopic(t *testing.T) {
	m := decodeMessage(t, `{
		"update_id": 1,
		"message": {
			"message_id": 42,
			"message_thread_id": 77,
			"is_topic_message": true,
			"from": {"id": 5},
			"chat": {"id": -1001234567890, "type": "supergroup", "title": "Team", "is_forum": true},
			"text": "how is auth going?"
		}
	}`)
	if got := m.ThreadID(); got != 77 {
		t.Fatalf("ThreadID = %d, want 77", got)
	}
	if !m.Chat.IsForum {
		t.Fatal("chat should decode as a forum")
	}
}

// A reply chain in an ordinary group also carries message_thread_id. Those
// must not get their own session — only real forum topics do.
func TestThreadIDIgnoresNonTopicThreads(t *testing.T) {
	m := decodeMessage(t, `{
		"update_id": 2,
		"message": {
			"message_id": 43,
			"message_thread_id": 12,
			"from": {"id": 5},
			"chat": {"id": -100999, "type": "supergroup"},
			"text": "a threaded reply"
		}
	}`)
	if got := m.ThreadID(); got != 0 {
		t.Fatalf("ThreadID = %d, want 0 for a non-topic thread", got)
	}
}

func TestThreadIDForDirectMessage(t *testing.T) {
	m := decodeMessage(t, `{
		"update_id": 3,
		"message": {
			"message_id": 44,
			"from": {"id": 5},
			"chat": {"id": 5, "type": "private"},
			"text": "hi"
		}
	}`)
	if got := m.ThreadID(); got != 0 {
		t.Fatalf("ThreadID = %d, want 0 for a DM", got)
	}
}

func TestTopicNameSources(t *testing.T) {
	created := decodeMessage(t, `{
		"update_id": 4,
		"message": {
			"message_id": 45,
			"message_thread_id": 77,
			"is_topic_message": true,
			"from": {"id": 5},
			"chat": {"id": -100999, "type": "supergroup", "is_forum": true},
			"forum_topic_created": {"name": "Authentication"}
		}
	}`)
	if got := created.TopicName(); got != "Authentication" {
		t.Fatalf("TopicName = %q, want %q", got, "Authentication")
	}
	if !created.IsForumService() {
		t.Fatal("forum_topic_created should be a service message")
	}

	reply := decodeMessage(t, `{
		"update_id": 5,
		"message": {
			"message_id": 46,
			"message_thread_id": 77,
			"is_topic_message": true,
			"from": {"id": 5},
			"chat": {"id": -100999, "type": "supergroup", "is_forum": true},
			"text": "still broken",
			"reply_to_message": {
				"message_id": 45,
				"message_thread_id": 77,
				"is_topic_message": true,
				"chat": {"id": -100999, "type": "supergroup"},
				"forum_topic_created": {"name": "Authentication"}
			}
		}
	}`)
	if got := reply.TopicName(); got != "Authentication" {
		t.Fatalf("TopicName from reply_to_message = %q, want %q", got, "Authentication")
	}
	if reply.IsForumService() {
		t.Fatal("a user message in a topic is not a service message")
	}

	renamed := decodeMessage(t, `{
		"update_id": 6,
		"message": {
			"message_id": 47,
			"message_thread_id": 77,
			"is_topic_message": true,
			"from": {"id": 5},
			"chat": {"id": -100999, "type": "supergroup", "is_forum": true},
			"forum_topic_edited": {"name": "Auth (v2)"}
		}
	}`)
	if got := renamed.TopicName(); got != "Auth (v2)" {
		t.Fatalf("TopicName after rename = %q, want %q", got, "Auth (v2)")
	}
	if !renamed.IsForumService() {
		t.Fatal("forum_topic_edited should be a service message")
	}
}
