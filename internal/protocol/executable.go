package protocol

import "strings"

// IsExecutableTextCapability identifies the bounded, text-only execution
// protocol. A matching name is not permission to install or execute a skill:
// each worker must explicitly opt in and validate its local active package.
func IsExecutableTextCapability(id string) bool {
	if id == "cap.text.uppercase" {
		return true
	}
	const prefix = "cap.learned.text."
	if !strings.HasPrefix(id, prefix) || len(id) <= len(prefix) || len(id) > 128 {
		return false
	}
	for _, ch := range id[len(prefix):] {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}
