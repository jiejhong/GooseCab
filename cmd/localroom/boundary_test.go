package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

const testRoom = "synthetic-local-room"

func fixtureToken(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func fixtureParticipant(kind, id, token string, expiry time.Time, read, write bool) participant {
	digest := sha256.Sum256([]byte(token))
	return participant{Kind: kind, ID: id, TokenSHA256: hex.EncodeToString(digest[:]),
		ExpiresAt: expiry, Read: read, Write: write}
}

func fixtureServer(t *testing.T, participants ...participant) (*httptest.Server, *roomServer) {
	t.Helper()
	return fixtureRoomServer(t, testRoom, participants...)
}

func fixtureRoomServer(t *testing.T, room string, participants ...participant) (*httptest.Server, *roomServer) {
	t.Helper()
	policy, err := newRoomPolicy(localConfig{Enabled: true, RoomID: room, Participants: participants})
	if err != nil {
		t.Fatal(err)
	}
	db, err := openRoomDB(filepath.Join(t.TempDir(), "state"), room)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	listener := httptest.NewUnstartedServer(nil)
	handler := newRoomServer(listener.Listener.Addr().String(), policy, db)
	listener.Config.Handler = handler
	listener.Start()
	t.Cleanup(listener.Close)
	return listener, handler
}

func fixtureEvent() protocol.Event {
	return protocol.Event{RoomID: testRoom, EventID: "synthetic-event", Type: "chat.message",
		Actor: protocol.Actor{Kind: "human", ID: "fixture-human"}, TS: time.Now(),
		Payload: map[string]string{"text": "synthetic persisted chat"}}
}

func TestPrivateStorageRejectsLegacyFormatAndRoomSwitch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "room-state")
	db, err := openRoomDB(dir, testRoom)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openRoomDB(dir, testRoom); err == nil {
		t.Fatal("second owner opened same directory")
	}
	if _, err := db.append(fixtureEvent(), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, dbFile))
	if err != nil || !bytes.HasPrefix(got, []byte(dbMagic)) || bytes.HasPrefix(got, []byte("SQLite format 3\x00")) {
		t.Fatal("persisted database has wrong format")
	}
	if _, err := openRoomDB(dir, "another-room"); err == nil {
		t.Fatal("another room accepted the persisted data")
	}
	reopened, err := openRoomDB(dir, testRoom)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.state.Events) != 1 || reopened.state.Events[0].Seq != 1 {
		t.Fatal("committed event did not survive reopen")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("SQLite format 3\x00")
	if err := os.WriteFile(filepath.Join(dir, dbFile), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openRoomDB(dir, testRoom); err == nil {
		t.Fatal("local room opened legacy format")
	}
	unchanged, err := os.ReadFile(filepath.Join(dir, dbFile))
	if err != nil || !bytes.Equal(unchanged, legacy) {
		t.Fatal("local room altered unrelated format")
	}
}

func TestReadBoundaryAndServerBoundRoles(t *testing.T) {
	human, agent := fixtureToken(1), fixtureToken(2)
	expiry := time.Now().Add(time.Hour)
	srv, _ := fixtureServer(t,
		fixtureParticipant("user", "human-bound", human, expiry, true, true),
		fixtureParticipant("agent", "agent-bound", agent, expiry, true, false),
	)
	for _, tc := range []struct {
		path, token, actorKind, actorID string
		status                          int
	}{
		{"/api/events", "", "", "", 401},
		{"/api/events", fixtureToken(3), "", "", 401},
		{"/api/events", human, "human", "human-bound", 200},
		{"/api/events", agent, "", "", 401},
		{"/api/events?room_id=other", human, "", "", 400},
		{"/api/events?room_id=" + testRoom, human, "", "", 400},
		{"/api/events/export.html", human, "", "", 404},
		{"/api/rooms", human, "", "", 404},
		{"/api/rooms/presence", human, "", "", 404},
		{"/api/v1/agent/connect", agent, "", "", 404},
		{"/api/invocations", human, "", "", 404},
		{"/api/transfers", human, "", "", 404},
	} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.token != "" {
			req.Header.Set("Authorization", localScheme+" "+tc.token)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, res.StatusCode, tc.status)
		}
		if tc.actorID != "" {
			var data struct {
				Room  string         `json:"room_id"`
				Actor protocol.Actor `json:"actor"`
			}
			if err := json.NewDecoder(res.Body).Decode(&data); err != nil ||
				data.Room != testRoom || data.Actor != (protocol.Actor{Kind: tc.actorKind, ID: tc.actorID}) {
				t.Error("response did not use the configured room and actor role")
			}
		}
		_ = res.Body.Close()
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	req.Host = "invalid.local"
	req.Header.Set("Authorization", localScheme+" "+human)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatal("unexpected Host was accepted")
	}
}

func TestWebSocketReplayAndIdleExpiry(t *testing.T) {
	token := fixtureToken(4)
	expiry := time.Now().Add(2 * time.Second)
	srv, handler := fixtureServer(t, fixtureParticipant("agent", "bound-agent", token, expiry, true, false))
	handler.mu.Lock()
	_, err := handler.db.append(fixtureEvent(), nil)
	handler.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	connect := func(origin, token string) (*websocket.Conn, *http.Response, error) {
		header := http.Header{"Origin": []string{origin}}
		if token != "" {
			header.Set("Authorization", localScheme+" "+token)
		}
		return websocket.DefaultDialer.Dial(endpoint, header)
	}
	refused := func(origin, token string, status int) {
		t.Helper()
		c, res, err := connect(origin, token)
		if c != nil {
			_ = c.Close()
		}
		if res != nil {
			defer res.Body.Close()
		}
		if err == nil || res == nil || res.StatusCode != status {
			t.Fatal("unexpected websocket handshake outcome")
		}
	}
	refused(srv.URL, "", 401)
	refused("http://127.0.0.1:1", token, 403)
	conn, res, err := connect(srv.URL, token)
	if err != nil {
		if res != nil {
			_ = res.Body.Close()
		}
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(expiry.Add(time.Second))
	var replay protocol.Event
	if err := conn.ReadJSON(&replay); err != nil || replay.EventID != "synthetic-event" || replay.RoomID != testRoom {
		t.Fatalf("authorized replay failed: %v", err)
	}
	if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseAbnormalClosure) ||
		time.Now().Before(expiry) {
		t.Fatalf("socket did not close at credential expiry: %v", err)
	}
	refused(srv.URL, token, 401)
}

func TestV2UserAuthorKeepsExistingUnreadReceipts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, dbFile)
	event := fixtureEvent()
	event.Actor.Kind = "user"
	event.Seq = 1
	actor := protocol.Actor{Kind: "agent", ID: "worker"}
	human := protocol.Actor{Kind: "user", ID: "reader"}
	legacy := roomState{
		RoomID: testRoom,
		Events: []protocol.Event{event},
		Unread: map[string][]protocol.Actor{event.EventID: {actor, human}},
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]byte(dbMagic), encoded...)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}

	db, err := openRoomDB(dir, testRoom)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(unchanged, before) {
		t.Fatal("opening the prior v2 database rewrote it")
	}
	server := &roomServer{db: db, policy: &roomPolicy{room: testRoom}}
	reply, code := server.agentReadPage(actor, protocol.LocalRoomReadQuery{Limit: 1})
	if code != "" || reply.Page.Events[0].Actor != (protocol.Actor{Kind: "human", ID: event.Actor.ID}) ||
		reply.Receipt == nil || len(reply.Receipt.EventIDs) != 1 || reply.Receipt.EventIDs[0] != event.EventID {
		t.Fatal("old author or unread event identity was lost while reading the v2 state")
	}
	if err := db.acknowledge(actor, reply.Receipt.EventIDs); err != nil {
		t.Fatal(err)
	}
	if pending := db.state.Unread[event.EventID]; len(pending) != 1 || pending[0] != human {
		t.Fatal("acknowledging the Agent removed the existing human receipt")
	}
}
