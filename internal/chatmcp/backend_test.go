package chatmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/registry"
	"github.com/jiejhong/GooseCab/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayIntegration(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", "test-token")
	db, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := registry.New()
	h := gateway.Authenticate((&gateway.HistoryAPI{Store: db}).Handler((&gateway.WS{Store: db, Registry: reg}).Handler((&gateway.API{Registry: reg}).Handler())))
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/agent/connect", Room: "fixed-room", AgentID: "test-agent", Token: "test-token"}
	bad := cfg
	bad.Token = "wrong"
	if b, e := Connect(ctx, bad); e == nil {
		b.Close()
		t.Fatal("wrong token accepted")
	}
	b, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	a := SendArgs{MessageID: "test-agent-unique-1", Text: "hello", ReplyTo: "parent-event"}
	result, err := b.Send(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if result.(receipt).Status != "persisted" {
		t.Fatal(result)
	}
	if _, err = b.Send(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Text = "changed"
	if _, err = b.Send(ctx, a); err == nil {
		t.Fatal("changed duplicate accepted")
	}
	other := protocol.Event{EventID: "other-room-event", RoomID: "other-room", Type: "chat.message", Actor: protocol.Actor{Kind: "agent", ID: "other"}, TS: time.Now(), Payload: map[string]any{"text": "secret other room"}}
	// Publish an independent event directly through the store for room filtering.
	if err = db.Append(ctx, other); err != nil {
		t.Fatal(err)
	}
	page, err := b.Read(ctx, ReadArgs{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "secret other room") {
		t.Fatal("cross-room leak")
	}
	events := page.(map[string]any)["events"].([]any)
	count := 0
	for _, x := range events {
		m := x.(map[string]any)
		if m["type"] == "chat.message" {
			count++
			if m["causation_id"] != "parent-event" {
				t.Fatal(m)
			}
		}
	}
	if count != 1 {
		t.Fatal("duplicate persisted", count)
	}
	if _, err = b.Discover(ctx, DiscoverArgs{Kind: "mcp"}); err != nil {
		t.Fatal(err)
	}
}
func TestURLRestrictions(t *testing.T) {
	for _, u := range []string{"http://localhost/api/v1/agent/connect", "ws://user:pw@localhost/api/v1/agent/connect", "ws://localhost/other", "ws://localhost/api/v1/agent/connect?room_id=other", "ws://localhost/api/v1/agent/connect#secret", "ws://example.com/api/v1/agent/connect"} {
		if _, err := validate(Config{URL: u, Room: "r", AgentID: "a", Token: "token"}); err == nil {
			t.Fatal(u)
		}
	}
	if _, err := validate(Config{URL: "wss://example.com/api/v1/agent/connect", Room: "r", AgentID: "a", Token: "token"}); err != nil {
		t.Fatal(err)
	}
}
func TestHTTPResponseLimitAndNoRedirect(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true); fmt.Fprint(w, "{}") }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing bearer")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL, 302)
			return
		}
		fmt.Fprint(w, strings.Repeat("x", MaxResponseBytes+1))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	b := &GatewayBackend{base: u, config: Config{Token: "token"}, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("blocked") }}}
	if _, err := b.get(context.Background(), "/large", nil); err == nil {
		t.Fatal("large response accepted")
	}
	if _, err := b.get(context.Background(), "/redirect", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if followed.Load() {
		t.Fatal("redirect followed")
	}
}
