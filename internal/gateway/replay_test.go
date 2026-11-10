package gateway

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/store"
)

func TestWebSocketReplayPreservesLiveEnvelope(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer((&WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler()))
	defer srv.Close()
	base := "ws" + srv.URL[4:]
	publisher, _, err := websocket.DefaultDialer.Dial(base+"/api/v1/agent/connect?room_id=demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	frames := []map[string]any{
		{"type": "agent.hello", "agent_id": "worker", "role": "worker", "capabilities": []string{"cap.echo"}},
		{"type": "capability.invoke", "request_id": "request-1", "capability_id": "cap.echo", "target_agent_id": "worker"},
		{"type": "capability.progress", "request_id": "request-1", "percent": 50},
		{"type": "capability.result", "request_id": "request-1", "status": "completed", "output_ref": "file:///worker/out.json"},
		{"type": "event.publish", "event": map[string]any{
			"room_id": "demo", "type": "task.completed", "actor": map[string]any{"kind": "agent", "id": "worker"},
			"task_id": "task-1", "capability_id": "cap.echo", "session_id": "session-1",
			"correlation_id": "request-1", "causation_id": "result-1",
			"payload": map[string]any{"output_ref": "file:///worker/out.json"},
		}},
	}
	for _, frame := range frames {
		if err = publisher.WriteJSON(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err = publisher.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var live []map[string]any
	for len(live) < 6 { // hello, automatic announce, invoke, progress, result, custom event
		var msg map[string]any
		if err = publisher.ReadJSON(&msg); err != nil {
			t.Fatal(err)
		}
		if msg["type"] != "agent.welcome" {
			live = append(live, msg)
		}
	}
	if live[2]["correlation_id"] != "request-1" || live[2]["capability_id"] != "cap.echo" {
		t.Fatalf("live invoke did not contain routing metadata: %#v", live[2])
	}
	if err = publisher.Close(); err != nil {
		t.Fatal(err)
	}

	// Both observer and Agent reconnect paths must replay the same envelope,
	// with an exclusive cursor rather than repeating the acknowledged hello.
	cursor := strconv.FormatInt(int64(live[0]["seq"].(float64)), 10)
	for _, path := range []string{"/ws", "/api/v1/agent/connect"} {
		t.Run(path, func(t *testing.T) {
			c, _, err := websocket.DefaultDialer.Dial(base+path+"?room_id=demo&resume_seq="+cursor, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err = c.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			for _, want := range live[1:] {
				var got map[string]any
				if err = c.ReadJSON(&got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("replay differs from live event:\n got %#v\nwant %#v", got, want)
				}
			}
		})
	}
}
