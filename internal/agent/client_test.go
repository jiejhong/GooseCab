package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/store"
)

func TestTwoAgentsAndLocalDataPlane(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := (&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	url := "ws" + srv.URL[4:] + "/api/v1/agent/connect"

	a, err := Dial(url, "smoke", "agent-a", []string{"cap.echo"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Dial(url, "smoke", "agent-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err = a.Write(map[string]any{
		"type":          "capability.announce",
		"agent_id":      "agent-a",
		"capability_id": "cap.echo",
	}); err != nil {
		t.Fatal(err)
	}
	if err = a.Write(map[string]any{
		"type":          "capability.invoke",
		"request_id":    "r1",
		"capability_id": "cap.echo",
		"payload":       "hello",
	}); err != nil {
		t.Fatal(err)
	}
	if err = b.Write(map[string]any{
		"type":       "capability.result",
		"request_id": "r1",
		"status":     "completed",
	}); err != nil {
		t.Fatal(err)
	}

	events := readExpectedEvents(t, a, map[string]struct{}{
		"agent.hello":         {},
		"capability.announce": {},
		"capability.invoke":   {},
		"capability.result":   {},
	})
	if actorID(events["agent.hello"]) != "agent-a" {
		t.Fatalf("agent hello actor mismatch: %#v", events["agent.hello"])
	}
	if actorID(events["capability.announce"]) != "agent-a" {
		t.Fatalf("capability announce actor mismatch: %#v", events["capability.announce"])
	}
	if payloadField(events["capability.invoke"], "request_id") != "r1" {
		t.Fatalf("capability invoke request mismatch: %#v", events["capability.invoke"])
	}
	if payloadField(events["capability.result"], "request_id") != "r1" || payloadField(events["capability.result"], "status") != "completed" {
		t.Fatalf("capability result mismatch: %#v", events["capability.result"])
	}

	src := filepath.Join(t.TempDir(), "source.bin")
	data := []byte("local-only payload for GooseCab")
	if err = os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	wantSHA := sha256.Sum256(data)
	wantHex := hex.EncodeToString(wantSHA[:])
	for _, mode := range []string{"direct", "relay"} {
		dst := filepath.Join(t.TempDir(), mode+"-target.bin")
		size, gotSHA, copyErr := CopyWithMode(mode, src, dst)
		if copyErr != nil {
			t.Fatalf("%s copy: %v", mode, copyErr)
		}
		if size != int64(len(data)) || gotSHA != wantHex {
			t.Fatalf("%s copy checksum mismatch: size=%d sha=%s", mode, size, gotSHA)
		}
		actual, readErr := os.ReadFile(dst)
		if readErr != nil {
			t.Fatalf("%s target: %v", mode, readErr)
		}
		if !bytes.Equal(actual, data) {
			t.Fatalf("%s target bytes differ", mode)
		}
	}
}

func TestDialWithToken(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", "test-token")
	s, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := (&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler())
	srv := httptest.NewServer(h)
	defer srv.Close()
	url := "ws" + srv.URL[4:] + "/api/v1/agent/connect"

	authClient, err := DialWithToken(url, "auth", "agent-auth", nil, "test-token")
	if err != nil {
		t.Fatalf("authenticated dial: %v", err)
	}
	defer authClient.Close()
	if _, err := Dial(url, "auth", "agent-no-token", nil); err == nil {
		t.Fatal("unauthenticated dial unexpectedly succeeded")
	}
}

func readExpectedEvents(t *testing.T, c *Client, expected map[string]struct{}) map[string]map[string]any {
	t.Helper()
	seen := make(map[string]map[string]any, len(expected))
	deadline := time.Now().Add(DefaultReadTimeout)
	for len(seen) < len(expected) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for events; got %v, want %v", eventKeys(seen), eventKeys(expected))
		}
		msg, err := c.ReadWithTimeout(remaining)
		if err != nil {
			t.Fatalf("read event (got %v, want %v): %v", eventKeys(seen), eventKeys(expected), err)
		}
		typ, _ := msg["type"].(string)
		if _, ok := expected[typ]; ok {
			if _, alreadySeen := seen[typ]; !alreadySeen {
				seen[typ] = msg
			}
		}
	}
	return seen
}

func actorID(event map[string]any) string {
	actor, _ := event["actor"].(map[string]any)
	id, _ := actor["id"].(string)
	return id
}

func payloadField(event map[string]any, field string) string {
	payload, _ := event["payload"].(map[string]any)
	value, _ := payload[field].(string)
	return value
}

func eventKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
