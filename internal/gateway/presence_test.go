package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

func TestGatewayRoomPresence(t *testing.T) {
	_, server, registry := securityServer(t)
	registry.RegisterHello("catalog-only", "Catalog entry", "", nil)
	securityDial(t, server, "/ws", "demo")
	uninitialized := securityDial(t, server, "/api/v1/agent/connect", "demo")
	expectProtocolError(t, uninitialized, map[string]any{"type": "agent.hello"}, "invalid_hello")

	await := func(room string, expected map[string]int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			req, err := http.NewRequest(http.MethodGet, server.URL+"/api/rooms/presence?room_id="+url.QueryEscape(room), nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer regression-token")
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var data struct {
				Room   string `json:"room_id"`
				Agents []struct {
					ID          string `json:"agent_id"`
					Connections int    `json:"connections"`
				} `json:"agents"`
			}
			err = json.NewDecoder(res.Body).Decode(&data)
			_ = res.Body.Close()
			if err != nil || res.StatusCode != http.StatusOK || data.Room != room {
				t.Fatalf("presence for %q: status=%d data=%+v error=%v", room, res.StatusCode, data, err)
			}
			got := make(map[string]int)
			for _, agent := range data.Agents {
				got[agent.ID] = agent.Connections
			}
			if reflect.DeepEqual(got, expected) && len(data.Agents) == len(expected) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("presence for %q: got %v, want %v", room, got, expected)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	await("demo", map[string]int{})
	first := securityDial(t, server, "/api/v1/agent/connect", "demo")
	writeFrame(t, first, map[string]any{"type": "agent.hello", "agent_id": "worker"})
	securityRead(t, first, "agent.welcome")
	await("demo", map[string]int{"worker": 1})

	second := securityDial(t, server, "/api/v1/agent/connect", "demo")
	writeFrame(t, second, map[string]any{"type": "agent.hello", "agent_id": "worker"})
	securityRead(t, second, "agent.welcome")
	other := securityDial(t, server, "/api/v1/agent/connect", "other")
	writeFrame(t, other, map[string]any{"type": "agent.hello", "agent_id": "worker"})
	securityRead(t, other, "agent.welcome")
	await("demo", map[string]int{"worker": 2})
	await("other", map[string]int{"worker": 1})

	_ = first.Close()
	await("demo", map[string]int{"worker": 1})
	_ = second.Close()
	await("demo", map[string]int{})
	await("other", map[string]int{"worker": 1})
}

func TestGatewayTracksSuccessfulAgentRoomActivity(t *testing.T) {
	_, server, _ := securityServer(t)
	connect := func(room string) *websocket.Conn {
		t.Helper()
		c := securityDial(t, server, "/api/v1/agent/connect", room)
		writeFrame(t, c, map[string]any{"type": "agent.hello", "agent_id": "shared-agent"})
		securityRead(t, c, "agent.welcome")
		return c
	}
	type row struct {
		ID          string     `json:"agent_id"`
		Connections int        `json:"connections"`
		Activity    *time.Time `json:"last_activity_at"`
	}
	await := func(room string, connections int, activity func(*time.Time) bool) row {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			request, err := http.NewRequest(http.MethodGet, server.URL+"/api/rooms/presence?room_id="+url.QueryEscape(room), nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer regression-token")
			res, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			var data struct {
				Room   string `json:"room_id"`
				Agents []row  `json:"agents"`
			}
			err = json.NewDecoder(res.Body).Decode(&data)
			_ = res.Body.Close()
			if err != nil || res.StatusCode != http.StatusOK || data.Room != room {
				t.Fatalf("presence response for %q: status %d, error %v", room, res.StatusCode, err)
			}
			if len(data.Agents) == 1 && data.Agents[0].ID == "shared-agent" &&
				data.Agents[0].Connections == connections && activity(data.Agents[0].Activity) {
				return data.Agents[0]
			}
			if time.Now().After(deadline) {
				t.Fatalf("presence for %q did not reach %d connections with expected activity: %+v", room, connections, data.Agents)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	none := func(at *time.Time) bool { return at == nil }
	first, second := connect("work"), connect("work")
	other := connect("other")
	await("work", 2, none)
	await("other", 1, none)
	chat := func(c *websocket.Conn, id, room, message string) {
		t.Helper()
		writeFrame(t, c, map[string]any{"type": "event.publish", "event": protocol.Event{
			EventID: id, RoomID: room, Type: "chat.message",
			TS: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Payload: map[string]string{"text": message},
		}})
	}
	waitEvent := func(c *websocket.Conn, id string) {
		t.Helper()
		for securityRead(t, c, "chat.message")["event_id"] != id {
		}
	}
	chat(first, "blank", "work", "　")
	waitEvent(first, "blank")
	await("work", 2, none)
	writeFrame(t, first, map[string]any{"type": "capability.announce", "capability_id": "cap.echo"})
	securityRead(t, first, "capability.announce")
	await("work", 2, none)

	started := time.Now()
	chat(first, "first", "work", "first message")
	waitEvent(first, "first")
	firstActivity := await("work", 2, func(at *time.Time) bool { return at != nil && !at.Before(started) }).Activity

	chat(second, "first", "work", "duplicate ID")
	duplicate := securityRead(t, second, "error")
	if duplicate["code"] != "persist_failed" {
		t.Fatalf("expected duplicate write to fail: %v", duplicate["code"])
	}
	chat(second, "wrong-room", "other", "unauthorized room")
	if wrong := securityRead(t, second, "error"); wrong["code"] != "room_mismatch" {
		t.Fatalf("expected mismatched room to fail: %v", wrong["code"])
	}
	await("work", 2, func(at *time.Time) bool { return at != nil && at.Equal(*firstActivity) })
	await("other", 1, none)

	chat(second, "second", "work", "second message")
	waitEvent(second, "second")
	last := await("work", 2, func(at *time.Time) bool { return at != nil && at.After(*firstActivity) }).Activity
	_ = first.Close()
	await("work", 1, func(at *time.Time) bool { return at != nil && at.Equal(*last) })
	await("other", 1, none)
	started = time.Now()
	writeFrame(t, other, map[string]any{"type": "transfer.request", "file_id": "synthetic-transfer"})
	securityRead(t, other, "transfer.request")
	await("other", 1, func(at *time.Time) bool { return at != nil && !at.Before(started) })
	await("work", 1, func(at *time.Time) bool { return at != nil && at.Equal(*last) })
}

func TestGatewayPresenceUsesRoomChatHistory(t *testing.T) {
	s, server, _ := securityServer(t)
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, event := range []protocol.Event{
		{EventID: "demo-chat", RoomID: "demo", Seq: 1, Type: "chat.message", TS: at, Payload: map[string]string{"text": "demo"}},
		{EventID: "other-chat", RoomID: "other", Seq: 1, Type: "chat.message", TS: at.Add(time.Hour), Payload: map[string]string{"text": "elsewhere"}},
	} {
		appendHistory(t, s, event)
	}
	query := func(room, auth string, code int) *time.Time {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/rooms/presence?room_id="+url.QueryEscape(room), nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != code {
			t.Fatalf("room %q status=%d, want %d", room, res.StatusCode, code)
		}
		if code != http.StatusOK {
			return nil
		}
		var data struct {
			Room      string     `json:"room_id"`
			Activity  *time.Time `json:"last_activity_at"`
			Available bool       `json:"activity_available"`
		}
		if err := json.NewDecoder(res.Body).Decode(&data); err != nil || !data.Available || data.Room != room {
			t.Fatalf("room %q: %+v, err=%v", room, data, err)
		}
		return data.Activity
	}
	const auth = "Bearer regression-token"
	query("demo", "", http.StatusUnauthorized)
	if got := query("empty", auth, http.StatusOK); got != nil {
		t.Fatalf("empty room had activity %s", got)
	}
	if got := query("demo", auth, http.StatusOK); got == nil || !got.Equal(at) {
		t.Fatalf("got %v, want first demo chat %s", got, at)
	}
	conn := securityDial(t, server, "/api/v1/agent/connect", "demo")
	writeFrame(t, conn, map[string]any{"type": "agent.hello", "agent_id": "worker"})
	securityRead(t, conn, "agent.welcome")
	if got := query("demo", auth, http.StatusOK); got == nil || !got.Equal(at) {
		t.Fatalf("hello changed activity to %v", got)
	}
	next := at.Add(time.Minute)
	writeFrame(t, conn, map[string]any{"type": "event.publish", "event": protocol.Event{
		Type: "chat.message", TS: next, Payload: map[string]string{"text": "new demo chat"},
	}})
	for {
		msg := securityRead(t, conn, "chat.message")
		if seq, _ := msg["seq"].(float64); seq > 1 {
			break
		}
	}
	if got := query("demo", auth, http.StatusOK); got == nil || !got.Equal(next) {
		t.Fatalf("got %v, want latest demo chat %s", got, next)
	}
}
