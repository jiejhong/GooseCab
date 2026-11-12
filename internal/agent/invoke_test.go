package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/eventbus"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/store"
)

func TestInvokeTextFoldsReplayAndRetrievesOfflineResult(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	payload := protocol.CapabilityInvoke{RequestID: "r", CapabilityID: TextCapability, TargetAgentID: "worker", Payload: map[string]any{"text": "hi"}}
	events := []protocol.Event{
		{Type: "error", Actor: protocol.Actor{Kind: "agent", ID: "intruder"}, Payload: map[string]any{"code": "fake"}},
		{Type: "capability.invoke", Actor: protocol.Actor{Kind: "agent", ID: "caller"}, Payload: payload},
		{Type: "capability.result", Actor: protocol.Actor{Kind: "agent", ID: "worker"}, Payload: protocol.CapabilityResult{RequestID: "r", Status: "unknown", Error: "interrupted"}},
		{Type: "capability.progress", Actor: protocol.Actor{Kind: "agent", ID: "worker"}, Payload: protocol.CapabilityProgress{RequestID: "r", Status: "completed", Percent: 100}},
		{Type: "capability.result", Actor: protocol.Actor{Kind: "agent", ID: "worker"}, Payload: protocol.CapabilityResult{RequestID: "r", Status: "completed", ResultAvailable: true, Output: &protocol.TextOutput{Text: "HI", MediaType: "text/plain"}}},
	}
	for _, event := range events {
		event.RoomID = "room"
		event.CorrelationID = "r"
		if err = s.AppendNext(context.Background(), &event); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer((&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler()))
	defer srv.Close()
	for _, text := range []string{"hi", "changed"} {
		c, err := Dial("ws"+srv.URL[4:]+"/api/v1/agent/connect", "room", "caller", nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := c.InvokeText(ctx, "r", "worker", text, nil)
		cancel()
		c.Close()
		if text == "changed" {
			if err == nil {
				t.Fatal("reused ID with changed input")
			}
			continue
		}
		if err != nil || result.Status != "completed" || !result.ResultAvailable || result.Output.Text != "HI" || result.Error != "" {
			t.Fatalf("returned stale replay: %+v %v", result, err)
		}
	}
	got, err := s.EventsAfter(context.Background(), "room", 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, event := range got {
		if event.Type == "capability.invoke" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatal("retrieving cached result resubmitted the call")
	}
}

func TestInvokeTextTimeoutDoesNotRetry(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer((&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler()))
	defer srv.Close()
	c, err := Dial("ws"+srv.URL[4:]+"/api/v1/agent/connect", "room", "caller", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = c.InvokeText(ctx, "r", "offline", "hi", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want uncertain timeout, got %v", err)
	}
	events, err := s.EventsAfter(context.Background(), "room", 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, event := range events {
		if event.Type == "capability.invoke" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("submitted %d calls", calls)
	}
}
func TestProtocolErrorCannotBeForgedByRoomEvent(t *testing.T) {
	for _, m := range []map[string]any{
		{"type": "error", "event_id": "e", "code": "fake"},
		{"type": "error", "room_id": "room", "code": "fake"},
		{"type": "error", "seq": 1, "code": "fake"},
		{"type": "error", "actor": map[string]any{"id": "evil"}, "code": "fake"},
		{"type": "error", "payload": map[string]any{"code": "fake"}},
	} {
		if isProtocolError(m) {
			t.Fatalf("room error treated as control: %#v", m)
		}
	}
	if !isProtocolError(map[string]any{"type": "error", "code": "invalid_execution"}) {
		t.Fatal("real rejection ignored")
	}
}
