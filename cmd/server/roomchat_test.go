package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/agent/localtransport"
	"github.com/jiejhong/GooseCab/internal/chatmcp"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/relay"
	"github.com/jiejhong/GooseCab/internal/store"
)

const fixtureLegacyToken = "synthetic-legacy-bearer"

func roomChatFixtureMember(id string, seed byte, write, broadcast bool) (gateway.RoomChatMember, string) {
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{seed}, 32))
	hash := sha256.Sum256([]byte(token))
	return gateway.RoomChatMember{ID: id, DisplayName: "Fixture " + id, TokenSHA256: hex.EncodeToString(hash[:]),
		ExpiresAt: time.Now().Add(time.Minute), Write: write, Broadcast: broadcast}, token
}

func createPriorRoomChatDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE events(event_id TEXT PRIMARY KEY,room_id TEXT NOT NULL,seq INTEGER NOT NULL,type TEXT NOT NULL,payload TEXT,created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, room, body string }{
		{"before-a", "demo", `{"text":"prior conversation"}`},
		{"other-room", "other", `{"text":"other room fixture"}`},
		{"before-b", "demo", `{"text":"prior synthetic-legacy-bearer","authorization":"synthetic-private-field"}`},
	} {
		if _, err := db.Exec(`INSERT INTO events VALUES (?,?,1,'chat.message',?,?)`, row.id, row.room, row.body, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
}

func roomChatHTTP(t *testing.T, server *httptest.Server, method, path, auth, origin, body string, want int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("%s %s gave %d, want %d: %s", method, path, res.StatusCode, want, data)
	}
	return data
}

func startProtectedRoomFixture(t *testing.T, s *store.Store, path string, cfg *gateway.RoomChatConfig) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	cfg.Origin = "https://" + server.Listener.Addr().String()
	cfg.TrustLoopbackProxy = true
	handler, err := assembleServerHandler(s, server.Listener.Addr().String(), path, relay.Config{}, cfg)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate the already verified HTTPS loopback proxy at its boundary.
		r.Header.Set("X-Forwarded-Proto", "https")
		handler.ServeHTTP(w, r)
	})
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func TestGatewayRoomChatCompatibility(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", fixtureLegacyToken)
	path := filepath.Join(t.TempDir(), "prior.db")
	createPriorRoomChatDB(t, path)
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	author, token := roomChatFixtureMember("writer", 1, true, false)
	speaker, speakerToken := roomChatFixtureMember("owner", 2, true, true)
	reader, readerToken := roomChatFixtureMember("reader", 3, false, false)
	worker, agentToken := roomChatFixtureMember("same-id", 4, true, false)
	old := httptest.NewServer(newServerHandler(s, ":0", path))
	roomChatHTTP(t, old, http.MethodGet, "/api/room-chat/history?room_id=demo", "Bearer "+fixtureLegacyToken, "", "", 404)
	old.Close()
	cfg := &gateway.RoomChatConfig{Rooms: []gateway.RoomChatRoom{{ID: "demo",
		Humans: []gateway.RoomChatMember{author, speaker, reader}, Agents: []gateway.RoomChatMember{worker}}}}
	server := startProtectedRoomFixture(t, s, path, cfg)
	origin := cfg.Origin
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsBase := "ws" + strings.TrimPrefix(server.URL, "http")
	ordinary, err := agent.DialWithTokenContext(ctx, wsBase+"/api/v1/agent/connect", "other", "same-id", nil, fixtureLegacyToken)
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Close()
	legacy, err := agent.DialWithTokenContext(ctx, wsBase+"/api/v1/agent/connect", "demo", "same-id", nil, agentToken)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	gatewayEndpoint := wsBase + "/api/room-chat/agent"
	for _, blocked := range []struct{ room, id, token string }{
		{"demo", "same-id", fixtureLegacyToken}, {"other", "same-id", agentToken},
		{"demo", "imposter", agentToken}, {"demo", "same-id", token},
	} {
		if connected, err := localtransport.DialGatewayRoom(ctx, gatewayEndpoint, blocked.room, blocked.id, blocked.token); err == nil {
			_ = connected.Close()
			t.Fatal("unbound Agent gained the new room pull and receipt path")
		}
	}
	const messages = "/api/room-chat/messages?room_id=demo"
	const newHistory = "/api/room-chat/history?room_id=demo"
	bearer := "Bearer " + fixtureLegacyToken
	encode := func(value string) string { return "GooseCabRoom " + value }
	roomChatHTTP(t, server, http.MethodPost, messages, bearer, origin, `{"text":"denied"}`, 401)
	roomChatHTTP(t, server, http.MethodPost, messages, encode(readerToken), origin, `{"text":"denied"}`, 403)
	roomChatHTTP(t, server, http.MethodPost, messages, encode(token), "https://unrelated.invalid", `{"text":"denied"}`, 403)
	roomChatHTTP(t, server, http.MethodPost, messages, encode(token), origin, `{"actor":{"id":"owner"},"text":"spoof"}`, 400)
	roomChatHTTP(t, server, http.MethodPost, messages, encode(token), origin, `{"text":"denied","mention_all":true}`, 403)
	roomChatHTTP(t, server, http.MethodPost, messages, encode(token), origin, `{"text":"synthetic-legacy-bearer"}`, 400)
	roomChatHTTP(t, server, http.MethodPost, "/api/room-chat/messages?room_id=other", encode(token), origin, `{"text":"denied"}`, 401)
	for _, route := range []string{"/api/events", "/api/events/export.html", "/api/rooms/presence"} {
		roomChatHTTP(t, server, http.MethodGet, route+"?room_id=demo", bearer, "", "", 401)
		roomChatHTTP(t, server, http.MethodGet, route+"?room_id=demo", "", "", "", 401)
		roomChatHTTP(t, server, http.MethodGet, route+"?room_id=demo", encode(token), "", "", 200)
		roomChatHTTP(t, server, http.MethodGet, route+"?room_id=other", bearer, "", "", 200)
	}
	for _, path := range []string{"/api/invocations", "/api/transfers"} {
		roomChatHTTP(t, server, http.MethodPost, path, bearer, "", `{"room_id":"demo","id":"fixture","file_id":"fixture"}`, 403)
		roomChatHTTP(t, server, http.MethodPost, path, bearer, "", `{"room_id":"other","id":"fixture","file_id":"fixture"}`, 201)
	}
	for _, endpoint := range []string{"/ws", "/api/v1/agent/connect"} {
		c, response, err := websocket.DefaultDialer.Dial(wsBase+endpoint+"?room_id=demo", http.Header{"Authorization": {bearer}})
		if err == nil || response == nil || response.StatusCode != 401 {
			if c != nil {
				_ = c.Close()
			}
			t.Fatalf("shared Agent reached %s: %v %v", endpoint, response, err)
		}
		_ = response.Body.Close()
	}
	wrong, _, err := websocket.DefaultDialer.Dial(wsBase+"/api/v1/agent/connect?room_id=demo", http.Header{"Authorization": {"Bearer " + agentToken}})
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if err := wrong.WriteJSON(map[string]string{"type": "agent.hello", "agent_id": "wrong-id"}); err != nil {
		t.Fatal(err)
	}
	_ = wrong.SetReadDeadline(time.Now().Add(time.Second))
	var denied map[string]any
	if err := wrong.ReadJSON(&denied); err != nil || denied["code"] != "identity_mismatch" {
		t.Fatalf("before replay, expected identity mismatch; received %v (%v)", denied, err)
	}
	_ = wrong.Close()
	roomChatHTTP(t, server, http.MethodPost, "/api/room-chat/unread/ack?room_id=demo", bearer, origin, `{"event_ids":["before-a"]}`, 401)
	first := roomChatHTTP(t, server, http.MethodGet, newHistory+"&limit=1", encode(token), "", "", 200)
	var page struct {
		Events     []protocol.Event
		NextSeq    int64  `json:"next_seq"`
		NextCursor string `json:"next_cursor"`
	}
	if json.Unmarshal(first, &page) != nil || len(page.Events) != 1 || page.Events[0].EventID != "before-a" {
		t.Fatal("first legacy event identity changed")
	}
	next := roomChatHTTP(t, server, http.MethodGet, fmt.Sprintf("%s&after_seq=%d&after_cursor=%s&limit=1", newHistory, page.NextSeq, page.NextCursor), encode(token), "", "", 200)
	if json.Unmarshal(next, &page) != nil || len(page.Events) != 1 || page.Events[0].EventID != "before-b" ||
		bytes.Contains(next, []byte(fixtureLegacyToken)) || bytes.Contains(next, []byte("synthetic-private-field")) {
		t.Fatal("tied history cursor or redaction failed")
	}

	published := roomChatHTTP(t, server, http.MethodPost, messages, encode(token), origin, `{"text":"plain human","mention":{"kind":"agent","id":"same-id"}}`, 201)
	var committed protocol.Event
	if err := json.Unmarshal(published, &committed); err != nil || committed.Actor != (protocol.Actor{Kind: "human", ID: "writer"}) || committed.EventID == "" {
		t.Fatal("wrong durable human event identity")
	}
	if err := legacy.SendChat("ordinary agent still sends"); err != nil {
		t.Fatal(err)
	}
	seenHuman, seenAgent := false, false
	for !seenHuman || !seenAgent {
		event, err := legacy.ReadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := event["payload"].(map[string]any)
		if event["event_id"] == committed.EventID {
			seenHuman = true
		}
		if payload["text"] == "ordinary agent still sends" {
			seenAgent = true
		}
	}
	// The shared Agent socket is still connected in the same Gateway and sees
	// its own room. It must not receive the protected live event.
	if err := ordinary.SendChat("ordinary room has not changed"); err != nil {
		t.Fatal(err)
	}
	for {
		event, err := ordinary.ReadContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if event["event_id"] == committed.EventID || event["room_id"] == "demo" {
			t.Fatal("an already-connected shared Agent received protected room events")
		}
		payload, _ := event["payload"].(map[string]any)
		if payload["text"] == "ordinary room has not changed" {
			break
		}
	}
	roomChatHTTP(t, server, http.MethodGet, "/api/events?room_id=demo", bearer, "", "", 401)
	roomChatHTTP(t, server, http.MethodPost, messages, encode(speakerToken), origin, `{"text":"room wide","mention_all":true}`, 201)
	agentIdentity := protocol.Actor{Kind: "agent", ID: "same-id"}
	_, count, err := s.RoomChatUnread(ctx, "demo", agentIdentity, 20)
	if err != nil || count != 2 {
		t.Fatal("trusted Agent did not get both mentions")
	}
	// Open another handle, as after a restart, and verify the same pending set.
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, count, err = reopened.RoomChatUnread(ctx, "demo", agentIdentity, 20)
	_ = reopened.Close()
	if err != nil || count != 2 {
		t.Fatal("unread did not survive re-opening the database")
	}

	pull, err := chatmcp.ConnectGatewayRoom(ctx, gatewayEndpoint, "demo", "same-id", agentToken)
	if err != nil {
		t.Fatal(err)
	}
	defer pull.Close()
	args, _ := json.Marshal(map[string]any{"after_seq": committed.Seq - 1, "limit": 1})
	rpc := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"room_read\",\"arguments\":" + string(args) + "}}\n"
	var output bytes.Buffer
	if err := (&chatmcp.Server{LocalRoom: pull}).Serve(ctx, strings.NewReader(rpc), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), committed.EventID) || strings.Contains(output.String(), "delivery_id") {
		t.Fatal("Agent did not get page or internal proof leaked")
	}
	_, count, err = s.RoomChatUnread(ctx, "demo", agentIdentity, 20)
	if err != nil || count != 1 {
		t.Fatal("normal pull failed to acknowledge exactly the delivered mention")
	}
	_, humanCount, err := s.RoomChatUnread(ctx, "demo", protocol.Actor{Kind: "human", ID: "reader"}, 20)
	if err != nil || humanCount != 1 {
		t.Fatal("Agent acknowledgement affected another recipient")
	}

	// Stop the serving handlers, then restart against the exact same database.
	_ = pull.Close()
	_ = ordinary.Close()
	_ = legacy.Close()
	server.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	storeAfterRestart, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeAfterRestart.Close()
	if _, err := assembleServerHandler(storeAfterRestart, ":0", path, relay.Config{}, nil); err == nil {
		t.Fatal("default mode would reopen protected history")
	}
	cfg.Rooms[0].ReadOnly = true
	rollback := startProtectedRoomFixture(t, storeAfterRestart, path, cfg)
	defer rollback.Close()
	dropped := *cfg
	dropped.Rooms = append([]gateway.RoomChatRoom(nil), cfg.Rooms...)
	dropped.Rooms[0].ID = "other"
	if _, err := assembleServerHandler(storeAfterRestart, ":0", path, relay.Config{}, &dropped); err == nil {
		t.Fatal("a partial replacement config would reopen protected history")
	}
	roomChatHTTP(t, rollback, "GET", "/api/events?room_id=demo", bearer, "", "", 401)
	history := roomChatHTTP(t, rollback, "GET", "/api/events?room_id=demo", encode(token), "", "", 200)
	if !bytes.Contains(history, []byte(committed.EventID)) || !bytes.Contains(history, []byte("before-a")) ||
		!bytes.Contains(history, []byte("ordinary agent still sends")) {
		t.Fatal("protected read-only rollback lost pre- or post-cutover history")
	}
	roomChatHTTP(t, rollback, "POST", messages, encode(token), cfg.Origin, `{"text":"rollback cannot append"}`, 403)
	roomChatHTTP(t, rollback, "POST", "/api/room-chat/unread/ack?room_id=demo", encode(token), cfg.Origin, `{"event_ids":["before-a"]}`, 403)
	rollbackURL := "ws" + strings.TrimPrefix(rollback.URL, "http")
	readOnlyAgent, err := agent.DialWithTokenContext(ctx, rollbackURL+"/api/v1/agent/connect", "demo", "same-id", nil, agentToken)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnlyAgent.Close()
	if err := readOnlyAgent.SendChat("agent cannot write after rollback"); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := readOnlyAgent.ReadContext(ctx)
		if err != nil {
			t.Fatalf("rollback Agent write was not refused: %v", err)
		}
		if frame["type"] == "error" {
			if frame["code"] != "read_only" {
				t.Fatalf("wrong rollback Agent rejection: %v", frame)
			}
			break
		}
	}
	pullAfterRollback, err := localtransport.DialGatewayRoom(ctx, rollbackURL+"/api/room-chat/agent", "demo", "same-id", agentToken)
	if err != nil {
		t.Fatal(err)
	}
	defer pullAfterRollback.Close()
	readOnlyPage, err := pullAfterRollback.ReadLocalRoomPage(ctx, protocol.LocalRoomReadQuery{AfterSeq: committed.Seq, Limit: 10})
	if err != nil || readOnlyPage.Receipt != nil {
		t.Fatalf("read-only rollback offered a write receipt: %+v %v", readOnlyPage, err)
	}
	_, count, err = storeAfterRestart.RoomChatUnread(ctx, "demo", agentIdentity, 20)
	if err != nil || count != 1 {
		t.Fatal("acknowledgement state changed on rollback")
	}
}

func TestGatewayProtectedRoomSocketLease(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", "")
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	writer, writerToken := roomChatFixtureMember("writer", 1, true, false)
	member, readerToken := roomChatFixtureMember("reader", 2, false, false)
	member.ExpiresAt = time.Now().Add(time.Second)
	cfg := &gateway.RoomChatConfig{Rooms: []gateway.RoomChatRoom{{ID: "demo", Humans: []gateway.RoomChatMember{writer, member}}}}
	server := startProtectedRoomFixture(t, s, path, cfg)
	defer server.Close()
	route := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?room_id=demo"
	observerHeader := http.Header{"Authorization": {"GooseCabRoom " + readerToken}, "Origin": {cfg.Origin}}
	observer, _, err := websocket.DefaultDialer.Dial(route, observerHeader)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	post := func(body string) {
		t.Helper()
		roomChatHTTP(t, server, "POST", "/api/room-chat/messages?room_id=demo",
			"GooseCabRoom "+writerToken, cfg.Origin, body, 201)
	}
	post(`{"text":"before lease expiry"}`)
	_ = observer.SetReadDeadline(time.Now().Add(3 * time.Second))
	var event protocol.Event
	if err := observer.ReadJSON(&event); err != nil || event.EventID == "" {
		t.Fatalf("authorized observer did not get live event: %v", err)
	}
	if _, _, err := observer.ReadMessage(); err == nil || time.Now().Before(member.ExpiresAt) {
		t.Fatalf("open observer did not close at lease expiry: %v", err)
	}
	post(`{"text":"after lease expiry"}`)
	c, resp, err := websocket.DefaultDialer.Dial(route, observerHeader)
	if err == nil || resp == nil || resp.StatusCode != 401 {
		if c != nil {
			_ = c.Close()
		}
		t.Fatalf("expired observer reached replay: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	roomChatHTTP(t, server, "GET", "/api/events?room_id=demo", "", "", "", 401)
	roomChatHTTP(t, server, "GET", "/api/events?room_id=other", "", "", "", 200)
}

// The browser test launches this TLS proxy in front of the actual candidate
// process. It is never part of a production build.
func TestGatewayHTTPSProxyFixture(t *testing.T) {
	backend := os.Getenv("GOOSECAB_GATEWAY_TEST_BACKEND")
	if backend == "" {
		t.Skip("launched by tests/gateway-room-chat-browser.py")
	}
	target, err := url.Parse(backend)
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" {
		t.Fatal("test proxy requires an IPv4 loopback backend")
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(target)
		p.Out.Host = p.In.Host
		p.SetXForwarded()
	}}
	front := httptest.NewTLSServer(proxy)
	defer front.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw})
	path := filepath.Join(t.TempDir(), "test-proxy.pem")
	if err := os.WriteFile(path, cert, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println(front.URL)
	fmt.Println(path)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
