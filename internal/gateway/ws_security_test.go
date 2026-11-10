package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/registry"
	"github.com/jiejhong/GooseCab/internal/store"
)

func securityServer(t *testing.T) (*store.Store, *httptest.Server, *registry.Registry) {
	t.Helper()
	t.Setenv("GOOSECAB_AGENT_TOKEN", "regression-token")
	s, err := store.Open(filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	reg := registry.New()
	srv := httptest.NewServer((&WS{Store: s, Bus: eventbus.New(), Registry: reg}).Handler((&API{Registry: reg}).Handler()))
	t.Cleanup(srv.Close)
	return s, srv, reg
}
func securityDial(t *testing.T, srv *httptest.Server, path, room string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+srv.URL[4:]+path+"?room_id="+url.QueryEscape(room), http.Header{"Authorization": []string{"Bearer regression-token"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func writeFrame(t *testing.T, c *websocket.Conn, frame any) {
	t.Helper()
	if err := c.WriteJSON(frame); err != nil {
		t.Fatal(err)
	}
}
func securityRead(t *testing.T, c *websocket.Conn, typ string) map[string]any {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var msg map[string]any
		if err := c.ReadJSON(&msg); err != nil {
			t.Fatal(err)
		}
		if msg["type"] == typ {
			return msg
		}
		if msg["type"] == "error" {
			t.Fatalf("unexpected protocol error: %v", msg)
		}
	}
}
func expectProtocolError(t *testing.T, c *websocket.Conn, frame any, code string) {
	t.Helper()
	writeFrame(t, c, frame)
	got := securityRead(t, c, "error")
	if got["code"] != code {
		t.Fatalf("error=%v, want %s", got, code)
	}
}
func storedEvents(t *testing.T, s *store.Store, room string) []protocol.Event {
	t.Helper()
	events, err := s.EventsAfter(context.Background(), room, 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestObserverIsReadOnlyForAllProtocolFrames(t *testing.T) {
	s, srv, reg := securityServer(t)
	c := securityDial(t, srv, "/ws", "observer")
	for _, typ := range []string{"agent.hello", "capability.announce", "capability.invoke", "capability.progress", "capability.result", "transfer.request", "transfer.accepted", "transfer.progress", "transfer.completed", "transfer.failed", "event.publish"} {
		expectProtocolError(t, c, map[string]any{"type": typ, "agent_id": "forged", "capability_id": "cap.forged", "event": map[string]any{"type": "chat.message", "room_id": "victim", "actor": map[string]string{"kind": "user", "id": "forged"}}}, "read_only")
	}
	if len(storedEvents(t, s, "observer")) != 0 || len(storedEvents(t, s, "victim")) != 0 || len(reg.List()) != 0 {
		t.Fatal("observer modified events or registry")
	}
}
func TestAgentConnectionBindsRoomAndIdentity(t *testing.T) {
	s, srv, reg := securityServer(t)
	c := securityDial(t, srv, "/api/v1/agent/connect", "own")
	for _, typ := range []string{"event.publish", "capability.announce", "capability.invoke", "capability.progress", "capability.result", "transfer.request"} {
		expectProtocolError(t, c, map[string]any{"type": typ}, "hello_required")
	}
	expectProtocolError(t, c, map[string]any{"type": "agent.hello", "agent_id": " "}, "invalid_hello")
	expectProtocolError(t, c, map[string]any{"type": "agent.hello", "agent_id": "worker", "capabilities": []string{""}}, "invalid_hello")
	writeFrame(t, c, map[string]any{"type": "agent.hello", "agent_id": "worker"})
	securityRead(t, c, "agent.welcome")
	expectProtocolError(t, c, map[string]any{"type": "agent.hello", "agent_id": "intruder"}, "already_initialized")
	expectProtocolError(t, c, map[string]any{"type": "capability.announce", "agent_id": "intruder", "capability_id": "cap.forged"}, "identity_mismatch")
	for _, tc := range []struct {
		event map[string]any
		code  string
	}{
		{map[string]any{"type": "note", "room_id": "victim"}, "room_mismatch"},
		{map[string]any{"type": "note", "actor": map[string]string{"kind": "agent", "id": "intruder"}}, "identity_mismatch"},
		{map[string]any{"type": "note", "actor": map[string]string{"kind": "user", "id": "worker"}}, "identity_mismatch"},
		{map[string]any{"type": "agent.hello"}, "reserved_event"},
		{map[string]any{"type": "capability.announce"}, "reserved_event"},
	} {
		expectProtocolError(t, c, map[string]any{"type": "event.publish", "event": tc.event}, tc.code)
	}
	writeFrame(t, c, map[string]any{"type": "event.publish", "event": map[string]any{"type": "note", "event_id": "explicit-id", "seq": 900}})
	msg := securityRead(t, c, "note")
	if msg["room_id"] != "own" || msg["seq"] != float64(2) || msg["actor"].(map[string]any)["id"] != "worker" {
		t.Fatal(msg)
	}
	expectProtocolError(t, c, map[string]any{"type": "event.publish", "event": map[string]any{"type": "note", "event_id": "explicit-id"}}, "persist_failed")
	if len(storedEvents(t, s, "own")) != 2 || len(storedEvents(t, s, "victim")) != 0 {
		t.Fatal("rejected messages reached storage")
	}
	if _, ok := reg.Get("intruder"); ok || len(reg.FindCapability("cap.forged")) != 0 {
		t.Fatal("forged identity reached registry")
	}
}
func TestHelloAnnounceDiscoveryAndReconnect(t *testing.T) {
	_, srv, reg := securityServer(t)
	c := securityDial(t, srv, "/api/v1/agent/connect", "one")
	writeFrame(t, c, map[string]any{"type": "agent.hello", "agent_id": "a", "name": "Alpha", "role": "worker", "capabilities": []string{"cap.echo", "cap.stale"}})
	securityRead(t, c, "agent.welcome")
	writeFrame(t, c, map[string]any{"type": "capability.announce", "capability_id": "cap.echo", "name": "Echo", "version": "2", "manifest": map[string]any{"schema": "v2"}})
	for {
		msg := securityRead(t, c, "capability.announce")
		p := msg["payload"].(map[string]any)
		if p["name"] == "Echo" {
			break
		}
	}
	got := reg.FindCapability("Echo")
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatal(got)
	}
	// Reconnect removes capabilities no longer declared, retains rich metadata,
	// and does not create duplicate registry entries or capabilities.
	c.Close()
	c = securityDial(t, srv, "/api/v1/agent/connect", "one")
	writeFrame(t, c, map[string]any{"type": "agent.hello", "agent_id": "a", "capabilities": []string{"cap.echo", "cap.echo"}})
	securityRead(t, c, "agent.welcome")
	a, ok := reg.Get("a")
	if !ok || a.Name != "Alpha" || a.Role != "worker" || len(a.Capabilities) != 1 || a.Capabilities[0].Version != "2" || len(reg.FindCapability("cap.stale")) != 0 {
		t.Fatal(a)
	}
}
func TestMultiRoomConcurrentPublishingAndReplay(t *testing.T) {
	s, srv, _ := securityServer(t)
	const writers = 12
	const messages = 10
	clients := make([]*websocket.Conn, writers)
	for i := range clients {
		room := fmt.Sprintf("room-%d", i%3)
		c := securityDial(t, srv, "/api/v1/agent/connect", room)
		clients[i] = c
		writeFrame(t, c, map[string]any{"type": "agent.hello", "agent_id": fmt.Sprintf("agent-%d", i)})
		securityRead(t, c, "agent.welcome")
	}
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i, c := range clients {
		wg.Add(1)
		go func(i int, c *websocket.Conn) {
			defer wg.Done()
			for n := 0; n < messages; n++ {
				if err := c.WriteJSON(map[string]any{"type": "event.publish", "event": map[string]any{"type": "note", "payload": map[string]int{"writer": i, "n": n}}}); err != nil {
					errs <- err
					return
				}
			}
			marker := fmt.Sprintf("barrier-%d", i)
			if err := c.WriteJSON(map[string]any{"type": "event.publish", "event": map[string]any{"type": marker}}); err != nil {
				errs <- err
				return
			}
			c.SetReadDeadline(time.Now().Add(10 * time.Second))
			for {
				var msg map[string]any
				if err := c.ReadJSON(&msg); err != nil {
					errs <- err
					return
				}
				if msg["type"] == marker {
					return
				}
				if msg["type"] == "error" {
					errs <- fmt.Errorf("protocol error: %v", msg)
					return
				}
			}
		}(i, c)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for roomIndex := 0; roomIndex < 3; roomIndex++ {
		room := fmt.Sprintf("room-%d", roomIndex)
		events := storedEvents(t, s, room)
		if len(events) != writers/3*(messages+2) {
			t.Fatalf("%s: %d events", room, len(events))
		}
		for i, e := range events {
			if e.Seq != int64(i+1) || ids[e.EventID] {
				t.Fatalf("duplicate ID or sequence: %+v", e)
			}
			ids[e.EventID] = true
		}
		after := int64(len(events) / 2)
		c, _, err := websocket.DefaultDialer.Dial("ws"+srv.URL[4:]+"/ws?room_id="+room+"&resume_seq="+strconv.FormatInt(after, 10), http.Header{"Authorization": []string{"Bearer regression-token"}})
		if err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		for _, want := range events[after:] {
			var got protocol.Event
			if err := c.ReadJSON(&got); err != nil {
				t.Fatal(err)
			}
			if got.EventID != want.EventID || got.Seq != want.Seq || got.Actor != want.Actor {
				t.Fatalf("replay mismatch: %+v want %+v", got, want)
			}
		}
		c.Close()
	}
}
func TestWebSocketRejectsInvalidRoomAndCursor(t *testing.T) {
	_, srv, _ := securityServer(t)
	for _, query := range []string{"", "room_id=", "room_id=a&room_id=b", "room_id=a&resume_seq=-1", "room_id=a&resume_seq=bad", "room_id=a&resume_seq=0&resume_seq=1"} {
		c, r, err := websocket.DefaultDialer.Dial("ws"+srv.URL[4:]+"/api/v1/agent/connect?"+query, http.Header{"Authorization": []string{"Bearer regression-token"}})
		if c != nil {
			c.Close()
		}
		if err == nil || r == nil || r.StatusCode != 400 {
			t.Fatalf("query %q status %v error %v", query, r, err)
		}
		r.Body.Close()
	}
}
