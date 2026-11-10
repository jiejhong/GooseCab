package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

const redactedValue = "[已隐藏]"

type historyRedactor struct{ token string }

func newHistoryRedactor(token string) historyRedactor { return historyRedactor{token: token} }

var (
	credentialText = regexp.MustCompile(`(?i)\b(bearer\s+)[^\s"'<>]+|(["']?(?:[a-z_-]*token|[a-z_-]*secret|password|passwd|api[_-]?key|authorization)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;"'<>]+)`)
	fileURI        = regexp.MustCompile(`(?i)file://[^\s"'<>]+`)
	localPath      = regexp.MustCompile(`(^|[\s"'(=:\[])(/[^/\s"'<>][^\s"'<>]*|[a-zA-Z]:[\\/][^\s"'<>]*|~[/\\][^\s"'<>]*|\\\\[^\s"'<>]+)`)
)

func (r historyRedactor) text(s string) string {
	if r.token != "" {
		s = strings.ReplaceAll(s, r.token, redactedValue)
	}
	s = credentialText.ReplaceAllString(s, "${1}${2}"+redactedValue)
	s = fileURI.ReplaceAllString(s, redactedValue)
	return localPath.ReplaceAllString(s, "${1}"+redactedValue)
}

func privateHistoryKey(key string) bool {
	key = strings.ToLower(key)
	key = strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(key)
	for _, marker := range []string{"token", "secret", "password", "passwd", "apikey", "authorization", "cookie", "credential", "privatekey", "accesskey"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return strings.Contains(key, "path") || strings.HasSuffix(key, "ref") || key == "database" || key == "filecontent" || key == "filebytes" || key == "filebase64"
}

func (r historyRedactor) value(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, value := range x {
			safeKey := r.text(key)
			if privateHistoryKey(key) {
				out[safeKey] = redactedValue
			} else {
				out[safeKey] = r.value(value)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = r.value(value)
		}
		return out
	case string:
		return r.text(x)
	default:
		return v
	}
}

func (r historyRedactor) event(event protocol.Event) (protocol.Event, error) {
	// Marshal once to normalize typed payloads, while preserving large numbers.
	b, err := json.Marshal(event)
	if err != nil {
		return protocol.Event{}, err
	}
	var v any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err = decoder.Decode(&v); err != nil {
		return protocol.Event{}, err
	}
	b, err = json.Marshal(r.value(v))
	if err != nil {
		return protocol.Event{}, err
	}
	var result protocol.Event
	decoder = json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	err = decoder.Decode(&result)
	return result, err
}
