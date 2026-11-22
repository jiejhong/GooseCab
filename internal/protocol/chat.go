package protocol

import "strings"

// HasChatText reports whether a JSON-decoded payload contains a nonblank body.
func HasChatText(payload any) bool {
	switch value := payload.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case map[string]any:
		for _, key := range []string{"text", "message", "content"} {
			if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		}
	}
	return false
}
