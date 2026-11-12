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

func TestDialReturnsHelloRejectionNotTimeout(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer((&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler()))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := DialWithTokenContext(ctx, "ws"+srv.URL[4:]+"/api/v1/agent/connect", "room", " ", nil, "")
	if c != nil {
		c.Close()
		t.Fatal("invalid hello unexpectedly connected")
	}
	var rejection *ProtocolError
	if !errors.As(err, &rejection) || rejection.Code != "invalid_hello" || rejection.Message == "" {
		t.Fatalf("expected explicit invalid_hello rejection, got %v", err)
	}
}

func TestDialPreservesRoomErrorEventBeforeWelcome(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evt := protocol.Event{RoomID: "room", Type: "error", Payload: map[string]any{"code": "application_error"}}
	if err = s.AppendNext(context.Background(), &evt); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&gateway.WS{Store: s, Bus: eventbus.New()}).Handler(http.NotFoundHandler()))
	defer srv.Close()
	c, err := Dial("ws"+srv.URL[4:]+"/api/v1/agent/connect", "room", "agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	msg, err := c.Read()
	if err != nil || msg["event_id"] != evt.EventID || msg["type"] != "error" {
		t.Fatalf("room error was not replayed: %#v, %v", msg, err)
	}
}
