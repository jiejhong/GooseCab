package protocol

import (
	"strings"
	"testing"
)

func TestExecutableTextNamespace(t *testing.T) {
	for _, id := range []string{"cap.text.uppercase", "cap.learned.text.trim_upper", "cap.learned.text.v1-test"} {
		if !IsExecutableTextCapability(id) {
			t.Errorf("rejected %q", id)
		}
	}
	for _, id := range []string{"", "cap.echo", "cap.learned.text.", "cap.learned.text./path", "cap.learned.text.\n", "cap.learned.text." + strings.Repeat("x", 128)} {
		if IsExecutableTextCapability(id) {
			t.Errorf("accepted %q", id)
		}
	}
}
