package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRoomClientExplicitDelivery(t *testing.T) {
	binary := os.Getenv("GOOSECAB_LOCALROOM_TEST_BINARY")
	if binary == "" {
		t.Skip("set GOOSECAB_LOCALROOM_TEST_BINARY to the built cmd/localroom for integration")
	}
	root := t.TempDir()
	policyPath := filepath.Join(root, "policy.json")
	state := filepath.Join(root, "state")
	expires := time.Now().Add(time.Minute).Format(time.RFC3339Nano)
	token := func(fill byte) string {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
	}
	person := func(kind, id, name, token string) map[string]any {
		digest := sha256.Sum256([]byte(token))
		return map[string]any{"kind": kind, "id": id, "display_name": name,
			"token_sha256": hex.EncodeToString(digest[:]), "expires_at": expires, "read": true, "write": true}
	}
	const room, worker = "synthetic-agent-room", "bound-worker"
	human, agent, anotherHuman := token(1), token(2), token(3)
	policy, err := json.Marshal(map[string]any{"enabled": true, "room_id": room, "agent_transport": true,
		"participants": []any{person("user", "writer", "测试房主", human), person("agent", worker, "测试 Agent", agent), person("user", "second-writer", "另一位测试成员", anotherHuman)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policy, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-config", policyPath, "-state", state, "-port", "0")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	started := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(pipe)
		_ = scan.Scan()
		started <- scan.Text()
	}()
	var base string
	select {
	case line := <-started:
		_, base, _ = strings.Cut(line, "listening at ")
	case <-time.After(5 * time.Second):
		t.Fatal("local integration server did not start")
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatal("fixture did not bind loopback")
	}
	endpoint := "ws" + strings.TrimPrefix(base, "http") + "/api/v1/agent/connect"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, invalid := range []struct{ endpoint, room, id, token string }{
		{endpoint, room, worker, human},
		{endpoint, "different-room", worker, agent},
		{endpoint, room, "claimed-other-agent", agent},
		{endpoint, room, worker, ""},
		{strings.ReplaceAll(endpoint, "127.0.0.1", "localhost"), room, worker, agent},
	} {
		if client, err := DialLocalRoomContext(ctx, invalid.endpoint, invalid.room, invalid.id, invalid.token); err == nil {
			_ = client.Close()
			t.Fatal("invalid identity, room or target was accepted")
		}
	}
	if client, err := DialWithTokenContext(ctx, endpoint, room, worker, nil, agent); err == nil {
		_ = client.Close()
		t.Fatal("legacy bearer was accepted for isolated Agent transport")
	}

	httpClient := &http.Client{Timeout: 3 * time.Second}
	call := func(method, path, credential, body string, status int) map[string]any {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "GooseCabLocal "+credential)
		if method == http.MethodPost {
			req.Header.Set("Origin", base)
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != status {
			t.Fatalf("%s %s returned %d, want %d", method, path, res.StatusCode, status)
		}
		var result map[string]any
		if status == http.StatusOK || status == http.StatusCreated {
			if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	delivered := call("POST", "/api/messages", human, `{"text":"synthetic mention","mention":{"kind":"agent","id":"bound-worker"}}`, 201)
	id := delivered["event_id"].(string)
	unread := func(want float64) {
		t.Helper()
		if page := call("GET", "/api/mentions", agent, "", 200); page["unread_count"] != want {
			t.Fatalf("unread count %v, want %v", page["unread_count"], want)
		}
	}
	unread(1)
	call("POST", "/api/mentions/read", agent, `{"event_ids":["`+id+`"]}`, 401)
	open := func() *Client {
		t.Helper()
		client, err := DialLocalRoomContext(ctx, endpoint, room, worker, agent)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	read := func(client *Client, kind string) map[string]any {
		t.Helper()
		message, err := client.ReadWithTimeout(2 * time.Second)
		if err != nil || message["type"] != kind {
			t.Fatalf("wanted %s, received %v (%v)", kind, message, err)
		}
		return message
	}
	first, second := open(), open()
	firstFrame, secondFrame := read(first, "chat.message"), read(second, "chat.message")
	proof1, proof2 := firstFrame["delivery_id"].(string), secondFrame["delivery_id"].(string)
	if firstFrame["event_id"] != id || secondFrame["event_id"] != id ||
		firstFrame["actor"].(map[string]any)["kind"] != "human" || secondFrame["actor"].(map[string]any)["kind"] != "human" || proof1 == proof2 {
		t.Fatal("replay did not deliver connection-specific proof")
	}
	if err := second.AcknowledgeLocalMention(id, proof1); err != nil {
		t.Fatal(err)
	}
	if msg := read(second, "error"); msg["code"] != "unconfirmed_delivery" {
		t.Fatal("stale session proof was not rejected")
	}
	unread(1)
	if err := first.Write(map[string]any{"type": "event.publish", "event": map[string]any{
		"type": "chat.message", "room_id": room, "actor": map[string]string{"kind": "human", "id": worker},
		"payload": map[string]string{"text": "spoofed human"},
	}}); err != nil {
		t.Fatal(err)
	}
	if msg := read(first, "error"); msg["code"] != "invalid_chat" {
		t.Fatal("authenticated Agent could publish a human actor")
	}
	if err := first.SendChat("synthetic reply"); err != nil {
		t.Fatal(err)
	}
	posted := read(first, "chat.message")
	_ = read(second, "chat.message")
	actor := posted["actor"].(map[string]any)
	payload := posted["payload"].(map[string]any)
	if actor["id"] != worker || actor["kind"] != "agent" || payload["author_display_name"] != "测试 Agent" {
		t.Fatal("Agent chat did not bind the configured identity")
	}
	unread(1)
	if err := first.AcknowledgeLocalMention(id, strings.Repeat("0", 32)); err != nil {
		t.Fatal(err)
	}
	if msg := read(first, "error"); msg["code"] != "unconfirmed_delivery" {
		t.Fatal("unproved delivery was accepted")
	}
	unread(1)
	if err := second.AcknowledgeLocalMention(id, proof2); err != nil {
		t.Fatal(err)
	}
	if ack := read(second, "mention.acknowledged"); ack["event_id"] != id {
		t.Fatal("explicit receipt lost the event ID")
	}
	unread(0)
	// A disk commit failure must produce no successful receipt and leave
	// the original installed state as unread for recovery.
	failed := call("POST", "/api/messages", anotherHuman,
		`{"text":"persist acknowledgement fixture","mention":{"kind":"agent","id":"bound-worker"}}`, 201)
	failedID := failed["event_id"].(string)
	failedDelivery := read(first, "chat.message")
	_ = read(second, "chat.message")
	if failedDelivery["event_id"] != failedID {
		t.Fatal("received the wrong fixture event")
	}
	path := filepath.Join(state, "room.gcdb")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "committed-copy")
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := first.AcknowledgeLocalMention(failedID, failedDelivery["delivery_id"].(string)); err != nil {
		t.Fatal(err)
	}
	if rejection := read(first, "error"); rejection["code"] != "ack_failed" {
		t.Fatal("failed write was acknowledged")
	}
	after, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("a failed acknowledgement changed committed room state")
	}
	dbLines := bytes.SplitN(after, []byte("\n"), 2)
	var persisted struct {
		Unread map[string]json.RawMessage `json:"unread_by_event"`
	}
	if len(dbLines) != 2 || json.Unmarshal(dbLines[1], &persisted) != nil || len(persisted.Unread[failedID]) == 0 {
		t.Fatal("failed acknowledgement removed its persisted receipt")
	}

}
