package protocol

import "testing"

func TestHasChatText(t *testing.T) {
	for _, test := range []struct {
		payload any
		want    bool
	}{
		{"legacy chat", true},
		{"　\t", false},
		{map[string]any{"text": "chat"}, true},
		{map[string]any{"message": "chat"}, true},
		{map[string]any{"content": "chat"}, true},
		{map[string]any{"text": "", "message": 1, "content": "　"}, false},
		{map[string]any{"status": "running"}, false},
		{nil, false},
	} {
		if got := HasChatText(test.payload); got != test.want {
			t.Errorf("HasChatText(%v) = %v, want %v", test.payload, got, test.want)
		}
	}
}
