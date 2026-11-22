package chatmcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

type localRoomFixture struct {
	base, endpoint, state, room string
	agent, other                string
	writers                     []string
}

func startReadFixture(t *testing.T) localRoomFixture {
	t.Helper()
	binary := os.Getenv("GOOSECAB_LOCALROOM_TEST_BINARY")
	if binary == "" {
		t.Skip("set GOOSECAB_LOCALROOM_TEST_BINARY to a built cmd/localroom")
	}
	root := t.TempDir()
	config, state := filepath.Join(root, "fixture-policy.json"), filepath.Join(root, "state")
	room := "mcp-receipt-fixtures"
	expires := time.Now().Add(time.Minute).Format(time.RFC3339Nano)
	var participants []map[string]any
	member := func(kind, id string, index byte, writable, broadcast bool) string {
		token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{index}, 32))
		sum := sha256.Sum256([]byte(token))
		participants = append(participants, map[string]any{
			"kind": kind, "id": id, "token_sha256": hex.EncodeToString(sum[:]),
			"expires_at": expires, "read": true, "write": writable, "broadcast": broadcast,
		})
		return token
	}
	f := localRoomFixture{room: room, state: state,
		agent: member("agent", "worker", 1, false, false), other: member("agent", "other", 2, false, false)}
	for i := 0; i < 5; i++ {
		f.writers = append(f.writers, member("user", fmt.Sprintf("writer-%d", i), byte(i+3), true, true))
	}
	data, err := json.Marshal(map[string]any{
		"enabled": true, "room_id": room, "agent_transport": true, "participants": participants,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-config", config, "-state", state, "-port", "0")
	pipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		_ = scanner.Scan()
		ready <- scanner.Text()
	}()
	select {
	case line := <-ready:
		_, f.base, _ = strings.Cut(line, "listening at ")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for isolated room")
	}
	if !strings.HasPrefix(f.base, "http://127.0.0.1:") {
		t.Fatal("isolated room did not bind loopback")
	}
	f.endpoint = "ws" + strings.TrimPrefix(f.base, "http") + "/api/v1/agent/connect"
	return f
}

func (f localRoomFixture) api(t *testing.T, method, path, token, body string, want int) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, f.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "GooseCabLocal "+token)
	if method == http.MethodPost {
		req.Header.Set("Origin", f.base)
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != want {
		t.Fatalf("%s %s: got %d, want %d", method, path, r.StatusCode, want)
	}
	var result map[string]any
	if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func (f localRoomFixture) unread(t *testing.T, token string, want float64) {
	t.Helper()
	mentions := f.api(t, http.MethodGet, "/api/mentions", token, "", http.StatusOK)
	if mentions["unread_count"] != want {
		t.Fatalf("unread count %v, want %v", mentions["unread_count"], want)
	}
}

func (f localRoomFixture) connect(t *testing.T) *LocalRoomReader {
	t.Helper()
	reader, err := ConnectLocalRoom(context.Background(), f.endpoint, f.room, "worker", f.agent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}

func (f localRoomFixture) heartbeat(t *testing.T) {
	t.Helper()
	endpoint := f.endpoint + "?room_id=" + url.QueryEscape(f.room)
	conn, response, err := websocket.DefaultDialer.Dial(endpoint, http.Header{"Authorization": {"GooseCabLocal " + f.agent}})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	defer conn.Close()
	expiry := time.Now().Add(2 * time.Second)
	_ = conn.SetReadDeadline(expiry)
	if err := conn.WriteJSON(map[string]string{"type": "agent.hello", "agent_id": "worker", "mode": protocol.LocalRoomPullMode}); err != nil {
		t.Fatal(err)
	}
	var welcome map[string]string
	if err := conn.ReadJSON(&welcome); err != nil || welcome["type"] != "agent.welcome" {
		t.Fatalf("missing heartbeat fixture welcome: %v", err)
	}
	gotPong := errors.New("fixture pong")
	conn.SetPongHandler(func(string) error { return gotPong })
	if err := conn.WriteControl(websocket.PingMessage, nil, expiry); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); !errors.Is(err, gotPong) {
		t.Fatalf("heartbeat was not observed: %v", err)
	}
}

func roomReadRequest(args string) string {
	return initRPC + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"room_read","arguments":` + args + "}}\n"
}

func readPageResult(t *testing.T, out *bytes.Buffer, wantError bool) (protocol.LocalRoomPage, string) {
	t.Helper()
	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 4096), 2*MaxResponseBytes)
	if !scan.Scan() || !scan.Scan() {
		t.Fatalf("MCP response missing: %v", scan.Err())
	}
	var rpc struct {
		Result struct {
			Error   bool                    `json:"isError"`
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(scan.Bytes(), &rpc); err != nil || len(rpc.Result.Content) != 1 || rpc.Result.Error != wantError {
		t.Fatalf("unexpected room_read response: %s", scan.Bytes())
	}
	if wantError {
		return protocol.LocalRoomPage{}, ""
	}
	var result struct {
		protocol.LocalRoomPage
		MentionReceipt string `json:"mention_receipt"`
	}
	text := rpc.Result.Content[0].Text
	if err := json.Unmarshal([]byte(text), &result); err != nil || strings.Contains(text, "\"delivery_id\"") {
		t.Fatalf("invalid tool page or leaked delivery proof: %v", err)
	}
	return result.LocalRoomPage, result.MentionReceipt
}

type failAfterInitialResponse struct {
	writes int
}

func (w *failAfterInitialResponse) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		return len(p), nil
	}
	return 0, io.ErrClosedPipe
}

type afterPageWriter struct {
	bytes.Buffer
	writes int
	after  func()
}

func (w *afterPageWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.writes++
	if w.writes == 2 {
		w.after()
	}
	return n, err
}

func TestRoomReadAutoConfirmsOnlyDeliveredPage(t *testing.T) {
	f := startReadFixture(t)
	const post = "/api/messages"
	direct := `{"text":"targeted fixture","mention":{"kind":"agent","id":"worker"}}`
	first := f.api(t, http.MethodPost, post, f.writers[0], direct, http.StatusCreated)
	second := f.api(t, http.MethodPost, post, f.writers[1], `{"text":"shared fixture","mention_all":true}`, http.StatusCreated)
	third := f.api(t, http.MethodPost, post, f.writers[2], direct, http.StatusCreated)
	reader := f.connect(t)
	f.unread(t, f.agent, 3) // connecting and getting welcome are not reads.
	f.heartbeat(t)
	f.unread(t, f.agent, 3)
	if wrong, err := ConnectLocalRoom(context.Background(), f.endpoint, "wrong-room", "worker", f.agent); err == nil {
		_ = wrong.Close()
		t.Fatal("cross-room Agent was accepted")
	}
	if err := (&Server{LocalRoom: reader}).Serve(context.Background(), strings.NewReader(roomReadRequest(`{"limit":1}`)), &failAfterInitialResponse{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("failed output was hidden: %v", err)
	}
	f.unread(t, f.agent, 3)

	run := func(args string, want any, remaining float64) protocol.LocalRoomPage {
		t.Helper()
		var out bytes.Buffer
		if err := (&Server{LocalRoom: reader}).Serve(context.Background(), strings.NewReader(roomReadRequest(args)), &out); err != nil {
			t.Fatal(err)
		}
		page, receipt := readPageResult(t, &out, false)
		if page.RoomID != f.room || len(page.Events) != 1 || page.Events[0].EventID != want ||
			page.Events[0].Actor.Kind != "human" || receipt != "pending_confirmation" {
			t.Fatalf("wrong page or receipt state: %+v %q", page, receipt)
		}
		f.unread(t, f.agent, remaining)
		return page
	}
	page := run(`{"after_seq":1,"limit":1}`, second["event_id"], 2)
	f.unread(t, f.other, 1) // acknowledging the same event cannot clear another Agent.
	run(fmt.Sprintf(`{"after_seq":%d,"after_cursor":%q,"limit":1}`, page.NextSeq, page.NextCursor), third["event_id"], 1)

	var invalid bytes.Buffer
	if err := (&Server{LocalRoom: reader}).Serve(context.Background(), strings.NewReader(roomReadRequest(`{"after_seq":1,"after_cursor":"3","limit":1}`)), &invalid); err != nil {
		t.Fatal(err)
	}
	_, _ = readPageResult(t, &invalid, true)
	f.unread(t, f.agent, 1)

	// The first event still has unread state. Disconnect after delivering a
	// successful MCP page, before the automatic confirmation can be sent.
	out := &afterPageWriter{after: func() { _ = reader.Close() }}
	err := (&Server{LocalRoom: reader}).Serve(context.Background(), strings.NewReader(roomReadRequest(`{"limit":1}`)), out)
	if err == nil || !strings.Contains(err.Error(), "mention acknowledgement unconfirmed") {
		t.Fatalf("disconnect after delivery was not observable: %v", err)
	}
	page, receipt := readPageResult(t, &out.Buffer, false)
	if page.Events[0].EventID != first["event_id"] || receipt != "pending_confirmation" {
		t.Fatal("a failed acknowledgement was presented as cleared")
	}
	f.unread(t, f.agent, 1)
	reader = f.connect(t)
	f.unread(t, f.agent, 1)
	run(`{"limit":1}`, first["event_id"], 0)
}

func TestRoomReadReceiptIsSessionBoundAndCommitFailureIsVisible(t *testing.T) {
	f := startReadFixture(t)
	for i := 0; i < 2; i++ {
		f.api(t, http.MethodPost, "/api/messages", f.writers[i],
			`{"text":"receipt fixture","mention":{"kind":"agent","id":"worker"}}`, http.StatusCreated)
	}
	reader, otherSession := f.connect(t), f.connect(t)
	ctx := context.Background()
	page, err := reader.client.ReadLocalRoomPage(ctx, protocol.LocalRoomReadQuery{Limit: 1})
	if err != nil || page.Receipt == nil {
		t.Fatalf("could not get fixture delivery: %v", err)
	}
	if _, err := otherSession.client.ReadLocalRoomPage(ctx, protocol.LocalRoomReadQuery{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if err := otherSession.client.ConfirmLocalRoomPage(ctx, *page.Receipt); err == nil {
		t.Fatal("an old connection's page proof was accepted")
	}
	f.unread(t, f.agent, 2)
	for i := 0; i < 2; i++ {
		if err := reader.client.ConfirmLocalRoomPage(ctx, *page.Receipt); err != nil {
			t.Fatalf("same connection receipt was not idempotent: %v", err)
		}
	}
	f.unread(t, f.agent, 1)

	path := filepath.Join(f.state, "room.gcdb")
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "last-commit")
	out := &afterPageWriter{after: func() {
		if err := os.Rename(path, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}}
	err = (&Server{LocalRoom: reader}).Serve(ctx, strings.NewReader(roomReadRequest(`{"after_seq":1,"limit":1}`)), out)
	if err == nil || !strings.Contains(err.Error(), "ack_failed") {
		t.Fatalf("commit failure was not visible: %v", err)
	}
	_, state := readPageResult(t, &out.Buffer, false)
	after, err := os.ReadFile(backup)
	if state != "pending_confirmation" || err != nil || !bytes.Equal(committed, after) {
		t.Fatal("failed acknowledgement damaged persistence or reported success")
	}
}
