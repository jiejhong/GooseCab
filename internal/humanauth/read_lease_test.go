package humanauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestReadLeaseRequiresReadGrantAndRespectsParent(t *testing.T) {
	reader, writer := syntheticToken(14), syntheticToken(15)
	expiry := time.Now().Add(time.Hour)
	a, err := New(Config{Enabled: true, Credentials: []Credential{
		credential("reader", reader, expiry, map[string]RoomGrants{"room": {Read: true}}),
		credential("writer", writer, expiry, map[string]RoomGrants{"room": {Write: true}}),
	}}, ReservedDigests{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.AuthorizeRead(bearer(writer), "room"); ok {
		t.Fatal("write grant yielded a read lease")
	}
	if _, ok := a.AuthorizeRead(bearer(reader), "other"); ok {
		t.Fatal("read grant yielded a cross-room lease")
	}
	lease, ok := a.AuthorizeRead(bearer(reader), "room")
	if !ok {
		t.Fatal("valid reader did not get a lease")
	}
	ctx, cancel := lease.Context(context.Background())
	defer cancel()
	if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(expiry) {
		t.Fatal("lease did not preserve the configured credential deadline")
	}

	parent, stop := context.WithDeadline(context.Background(), expiry.Add(-time.Minute))
	defer stop()
	child, end := lease.Context(parent)
	defer end()
	want, _ := parent.Deadline()
	if deadline, ok := child.Deadline(); !ok || !deadline.Equal(want) {
		t.Fatal("lease extended its parent's deadline")
	}
	stop()
	if !errors.Is(child.Err(), context.Canceled) {
		t.Fatal("parent cancellation did not cancel the lease")
	}
	zero, stopZero := (ReadLease{}).Context(context.Background())
	defer stopZero()
	if !errors.Is(zero.Err(), context.DeadlineExceeded) {
		t.Fatal("zero lease was not already expired")
	}
}

func TestReadLeaseClosesIdleLoopbackWebSocketAtExpiry(t *testing.T) {
	token := syntheticToken(16)
	expiry := time.Now().Add(2 * time.Second)
	a, err := New(Config{Enabled: true, Credentials: []Credential{
		credential("reader", token, expiry, map[string]RoomGrants{"room": {Read: true}}),
	}}, ReservedDigests{})
	if err != nil {
		t.Fatal(err)
	}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lease, ok := a.AuthorizeRead(r.Header.Values("Authorization"), "room")
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		ctx, cancel := lease.Context(r.Context())
		defer cancel()
		stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stopClose()
		if err := conn.WriteMessage(websocket.TextMessage, []byte("local test ready")); err != nil {
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	header := http.Header{"Authorization": bearer(token)}
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, res, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		if res != nil {
			_ = res.Body.Close()
		}
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetReadDeadline(expiry.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = conn.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseAbnormalClosure) {
		t.Fatalf("idle socket was not closed by the test server: %v", err)
	}
	if time.Now().Before(expiry) {
		t.Fatal("idle socket closed before the credential expired")
	}
	conn2, response, err := websocket.DefaultDialer.Dial(url, header)
	if conn2 != nil {
		_ = conn2.Close()
	}
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal("expired credential reopened a socket")
	}
}
