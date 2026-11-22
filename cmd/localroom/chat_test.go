package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

func sendFixture(t *testing.T, srvURL, path, origin, auth, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srvURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", contentType)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func TestMessageDenialsNeverPersist(t *testing.T) {
	now := time.Now()
	writer, reader, agent, expired, noRead := fixtureToken(10), fixtureToken(11), fixtureToken(12), fixtureToken(13), fixtureToken(14)
	srv, handler := fixtureServer(t,
		fixtureParticipant("user", "writer", writer, now.Add(time.Hour), true, true),
		fixtureParticipant("user", "reader", reader, now.Add(time.Hour), true, false),
		fixtureParticipant("agent", "agent", agent, now.Add(time.Hour), true, false),
		fixtureParticipant("user", "expired", expired, now.Add(-time.Hour), true, true),
		fixtureParticipant("user", "no-read", noRead, now.Add(time.Hour), false, false),
	)
	const valid = `{"text":"hello"}`
	for _, tc := range []struct {
		name, path, origin, auth, contentType, body string
		want                                        int
	}{
		{"anonymous", "/api/messages", srv.URL, "", "application/json", valid, 401},
		{"shared Bearer spelling", "/api/messages", srv.URL, "Bearer " + writer, "application/json", valid, 401},
		{"agent", "/api/messages", srv.URL, localScheme + " " + agent, "application/json", valid, 401},
		{"expired", "/api/messages", srv.URL, localScheme + " " + expired, "application/json", valid, 401},
		{"no room read", "/api/messages", srv.URL, localScheme + " " + noRead, "application/json", valid, 401},
		{"read only", "/api/messages", srv.URL, localScheme + " " + reader, "application/json", valid, 403},
		{"missing origin", "/api/messages", "", localScheme + " " + writer, "application/json", valid, 403},
		{"wrong origin", "/api/messages", "http://127.0.0.1:1", localScheme + " " + writer, "application/json", valid, 403},
		{"foreign room query", "/api/messages?room_id=another-room", srv.URL, localScheme + " " + writer, "application/json", valid, 404},
		{"foreign room body", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", `{"text":"hello","room_id":"other"}`, 400},
		{"claimed human", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", `{"text":"hello","actor":{"kind":"human","id":"someone-else"}}`, 400},
		{"claimed agent", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", `{"text":"hello","actor":{"kind":"agent","id":"agent"}}`, 400},
		{"form", "/api/messages", srv.URL, localScheme + " " + writer, "application/x-www-form-urlencoded", valid, 415},
		{"blank", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", `{"text":" \t "}`, 400},
		{"text limit counts bytes", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", `{"text":"` + strings.Repeat("汉", 1366) + `"}`, 400},
		{"body limit", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", strings.Repeat(" ", maxChatBody+1), 413},
		{"unknown target", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", `{"text":"hello","mention":{"kind":"agent","id":"other"}}`, 400},
		{"expired target", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", `{"text":"hello","mention":{"kind":"user","id":"expired"}}`, 400},
		{"no read target", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", `{"text":"hello","mention":{"kind":"user","id":"no-read"}}`, 400},
		{"broadcast without permission", "/api/messages", srv.URL, localScheme + " " + writer, "application/json", `{"text":"hello","mention_all":true}`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := sendFixture(t, srv.URL, tc.path, tc.origin, tc.auth, tc.contentType, tc.body)
			if res.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/messages", strings.NewReader(valid))
	req.SetBasicAuth("localroom", writer)
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Content-Type", "application/json")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ambient Basic auth returned %d", res.StatusCode)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.db.state.Events) != 0 {
		t.Fatal("a rejected write persisted")
	}
}

type viewEvent struct {
	protocol.Actor `json:"actor"`
	Payload        localChat `json:"payload"`
	Room           string    `json:"room_id"`
	ID             string    `json:"event_id"`
	Seq            int64     `json:"seq"`
}

func TestCommittedMessagesReachStreamAndHistory(t *testing.T) {
	expiry := time.Now().Add(time.Minute)
	first, second, agent := fixtureToken(20), fixtureToken(21), fixtureToken(22)
	srv, handler := fixtureServer(t,
		fixtureParticipant("user", "first", first, expiry, true, true),
		fixtureParticipant("user", "second", second, expiry, true, true),
		fixtureParticipant("agent", "recipient", agent, expiry, true, false),
	)
	endpoint := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(endpoint,
		http.Header{"Authorization": {localScheme + " " + agent}, "Origin": {srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	cases := []struct {
		token, id, encoded string
		chat               chatSubmission
	}{
		{first, "first", `{"text":"@所有人 <script>纯文本</script>"}`, chatSubmission{Text: "@所有人 <script>纯文本</script>"}},
		{second, "second", `{"text":"please review","mention":{"kind":"agent","id":"recipient"}}`,
			chatSubmission{Text: "please review", Mention: &protocol.Actor{Kind: "agent", ID: "recipient"}}},
	}
	for i, tc := range cases {
		res := sendFixture(t, srv.URL, "/api/messages", srv.URL, localScheme+" "+tc.token, "application/json", tc.encoded)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("receipt status %d", res.StatusCode)
		}
		var receipt, live viewEvent
		if err := json.NewDecoder(res.Body).Decode(&receipt); err != nil {
			t.Fatal(err)
		}
		if err := conn.ReadJSON(&live); err != nil {
			t.Fatal(err)
		}
		if receipt.Room != testRoom || receipt.ID == "" || receipt.Seq != int64(i+1) ||
			receipt.Actor != (protocol.Actor{Kind: "human", ID: tc.id}) || receipt.Payload.Text != tc.chat.Text ||
			live.ID != receipt.ID || live.Actor != receipt.Actor || live.Payload.Text != receipt.Payload.Text {
			t.Fatalf("unexpected receipt or live event: %+v %+v", receipt, live)
		}
		if (live.Payload.Mention == nil) != (tc.chat.Mention == nil) ||
			tc.chat.Mention != nil && (*live.Payload.Mention != *tc.chat.Mention ||
				receipt.Payload.Mention == nil || *receipt.Payload.Mention != *tc.chat.Mention) {
			t.Fatal("structured target was changed or dropped")
		}
		// Reading the file after receiving the live event must already include it.
		file, err := os.ReadFile(filepath.Join(handler.db.dir, dbFile))
		payload, ok := bytes.CutPrefix(file, []byte(dbMagic))
		var disk roomState
		if err != nil || !ok || decodeJSON(payload, &disk) != nil || len(disk.Events) != i+1 ||
			disk.Events[i].EventID != receipt.ID || disk.Events[i].Actor != receipt.Actor {
			t.Fatal("live event arrived without its durable actor and event ID")
		}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	req.Header.Set("Authorization", localScheme+" "+first)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var history struct {
		CanWrite bool        `json:"can_write"`
		Events   []viewEvent `json:"events"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&history) != nil ||
		!history.CanWrite || len(history.Events) != 2 || history.Events[0].Actor.Kind != "human" ||
		history.Events[1].Actor.Kind != "human" || history.Events[0].Payload.Mention == nil ||
		*history.Events[0].Payload.Mention != *cases[1].chat.Mention {
		t.Fatal("history lost a committed message or target")
	}
}

func TestWriteRateAndFailedDiskCommit(t *testing.T) {
	expiry := time.Now().Add(time.Minute)
	first, second := fixtureToken(23), fixtureToken(24)
	srv, handler := fixtureServer(t,
		fixtureParticipant("user", "first", first, expiry, true, true),
		fixtureParticipant("user", "second", second, expiry, true, true),
	)
	sub := handler.bus.SubscribeRoom(testRoom, 4)
	defer handler.bus.Unsubscribe(sub)
	post := func(token string, want int) {
		t.Helper()
		res := sendFixture(t, srv.URL, "/api/messages", srv.URL, localScheme+" "+token, "application/json", `{"text":"fixture"}`)
		if res.StatusCode != want {
			t.Fatalf("status %d, want %d", res.StatusCode, want)
		}
	}
	post(first, http.StatusCreated)
	select {
	case <-sub:
	default:
		t.Fatal("committed message was not published")
	}
	post(first, http.StatusTooManyRequests)
	dbPath := filepath.Join(handler.db.dir, dbFile)
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dbPath, 0700); err != nil {
		t.Fatal(err)
	}
	post(second, http.StatusServiceUnavailable)
	select {
	case evt := <-sub:
		t.Fatalf("uncommitted event was published: %v", evt.EventID)
	default:
	}
}

func TestDedicatedDirectoryRejectsUnrelatedState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "goosecab.db")
	if err := os.WriteFile(legacy, []byte("synthetic unrelated file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openRoomDB(dir, testRoom); err == nil {
		t.Fatal("accepted a non-dedicated directory")
	}
	if _, err := os.Stat(filepath.Join(dir, dbFile)); !os.IsNotExist(err) {
		t.Fatal("wrote protected data alongside an unrelated database")
	}
}

func TestIndependentRoomDoesNotAcceptAnotherRoomsSession(t *testing.T) {
	expiry := time.Now().Add(time.Minute)
	first, second := fixtureToken(30), fixtureToken(31)
	fixtureRoomServer(t, "first-room", fixtureParticipant("user", "same-id", first, expiry, true, true))
	other, _ := fixtureRoomServer(t, "second-room", fixtureParticipant("user", "same-id", second, expiry, true, true))
	req, _ := http.NewRequest(http.MethodGet, other.URL+"/api/events", nil)
	req.Header.Set("Authorization", localScheme+" "+first)
	res, err := other.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-instance read got %d", res.StatusCode)
	}
	res = sendFixture(t, other.URL, "/api/messages", other.URL, localScheme+" "+first,
		"application/json", `{"text":"fixture"}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-instance write got %d", res.StatusCode)
	}
}
