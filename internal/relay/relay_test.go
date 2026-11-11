package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T, cfg Config) (Client, *Server) {
	t.Helper()
	s := New(cfg)
	h := httptest.NewServer(s)
	t.Cleanup(h.Close)
	return Client{URL: h.URL}, s
}
func ticket(t *testing.T, c Client, b []byte) Ticket {
	t.Helper()
	h := sha256.Sum256(b)
	x, e := c.Create(context.Background(), "alice", "bob", int64(len(b)), hex.EncodeToString(h[:]))
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func TestStreamingRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, ChunkSize*5 + 17} {
		t.Run(string(rune(n+65)), func(t *testing.T) {
			c, _ := setup(t, Config{})
			b := bytes.Repeat([]byte("x"), n)
			x := ticket(t, c, b)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ch := make(chan error, 1)
			go func() { ch <- c.Send(ctx, x, bytes.NewReader(b)) }()
			var dst bytes.Buffer
			if e := c.Receive(ctx, x, &dst); e != nil {
				t.Fatal(e)
			}
			if e := <-ch; e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(dst.Bytes(), b) {
				t.Fatal("bytes differ")
			}
			if e := c.Send(ctx, x, bytes.NewReader(b)); e == nil {
				t.Fatal("ticket replay succeeded")
			}
		})
	}
}
func TestDigestFailureDoesNotInstall(t *testing.T) {
	c, _ := setup(t, Config{})
	x := ticket(t, c, []byte("good"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := make(chan error, 1)
	go func() { ch <- c.Send(ctx, x, strings.NewReader("evil")) }()
	dir := t.TempDir()
	if _, e := c.ReceiveFile(ctx, x, dir, "out"); e == nil {
		t.Fatal("digest accepted")
	}
	if e := <-ch; e == nil {
		t.Fatal("sender received false success")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("failed transfer leaked files")
	}
}
func TestNoOverwriteAndTraversal(t *testing.T) {
	c, _ := setup(t, Config{})
	x := ticket(t, c, []byte("new"))
	dir := t.TempDir()
	if _, e := c.ReceiveFile(context.Background(), x, dir, "../escape"); e == nil {
		t.Fatal("path accepted")
	}
	dest := filepath.Join(dir, "out")
	if e := os.WriteFile(dest, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := make(chan error, 1)
	go func() { ch <- c.Send(ctx, x, strings.NewReader("new")) }()
	if _, e := c.ReceiveFile(ctx, x, dir, "out"); e == nil {
		t.Fatal("overwrite accepted")
	}
	if e := <-ch; e == nil {
		t.Fatal("sender falsely acknowledged failed installation")
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "old" {
		t.Fatal("overwritten")
	}
}
func TestCapacityExpiryAndCancellation(t *testing.T) {
	c, s := setup(t, Config{MaxSessions: 1, Timeout: 70 * time.Millisecond})
	x := ticket(t, c, nil)
	if _, e := c.Create(context.Background(), x.Sender, x.Recipient, 0, x.SHA256); e == nil {
		t.Fatal("capacity ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- c.Receive(ctx, x, io.Discard) }()
	cancel()
	select {
	case e := <-ch:
		if e == nil {
			t.Fatal("cancel ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel hung")
	}
	time.Sleep(100 * time.Millisecond)
	s.mu.Lock()
	n := len(s.sessions)
	s.mu.Unlock()
	if n != 0 {
		t.Fatal("session not expired")
	}
	ticket(t, c, nil)
}
func raw(t *testing.T, c Client, x Ticket, secret, agent, role string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{"X-Relay-ID": {x.ID}, "X-Relay-Role": {role}, "X-Relay-Secret": {secret}, "X-Relay-Agent": {agent}}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(c.URL, "http")+"/api/v1/relay/connect", h)
}
func TestSecretBindingAndDuplicate(t *testing.T) {
	c, _ := setup(t, Config{Timeout: time.Second})
	x := ticket(t, c, nil)
	for _, p := range [][2]string{{"wrong", x.Sender}, {x.SendSecret, "mallory"}} {
		conn, resp, e := raw(t, c, x, p[0], p[1], "send")
		if e == nil {
			conn.Close()
			t.Fatal("invalid binding accepted")
		}
		if resp.StatusCode != 403 {
			t.Fatal(resp.StatusCode)
		}
		resp.Body.Close()
	}
	conn, _, e := raw(t, c, x, x.SendSecret, x.Sender, "send")
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	_, resp, e := raw(t, c, x, x.SendSecret, x.Sender, "send")
	if e == nil || resp.StatusCode != 409 {
		t.Fatal("duplicate accepted")
	}
	resp.Body.Close()
}
func TestOversizedFrameRejected(t *testing.T) {
	c, _ := setup(t, Config{Timeout: time.Second})
	x := ticket(t, c, bytes.Repeat([]byte("x"), ChunkSize+1))
	conn, _, e := raw(t, c, x, x.SendSecret, x.Sender, "send")
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ch := make(chan error, 1)
	go func() { ch <- c.Receive(context.Background(), x, io.Discard) }()
	if e = expect(conn, "ready"); e != nil {
		t.Fatal(e)
	}
	_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, ChunkSize+1))
	if e = <-ch; e == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestShortAndLongSourcesFail(t *testing.T) {
	for _, src := range []string{"a", "abc"} {
		c, _ := setup(t, Config{Timeout: 100 * time.Millisecond})
		x := ticket(t, c, []byte("ab"))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ch := make(chan error, 1)
		go func() { ch <- c.Send(ctx, x, strings.NewReader(src)) }()
		if e := c.Receive(ctx, x, io.Discard); e == nil {
			t.Fatal("bad size accepted")
		}
		if e := <-ch; e == nil {
			t.Fatal("bad sender size accepted")
		}
		cancel()
	}
}
func TestFinalizerFailureRejectsSender(t *testing.T) {
	c, _ := setup(t, Config{Timeout: time.Second})
	x := ticket(t, c, []byte("ab"))
	ch := make(chan error, 1)
	go func() { ch <- c.Send(context.Background(), x, strings.NewReader("ab")) }()
	called := false
	e := c.ReceiveAndFinalize(context.Background(), x, io.Discard, func() error { called = true; return io.ErrClosedPipe })
	if e != io.ErrClosedPipe || !called {
		t.Fatalf("finalizer: %v, %v", called, e)
	}
	if e := <-ch; e == nil {
		t.Fatal("sender received success after finalizer failed")
	}
}
func TestWaitingPeerDeadline(t *testing.T) {
	c, _ := setup(t, Config{Timeout: 50 * time.Millisecond})
	x := ticket(t, c, nil)
	start := time.Now()
	if e := c.Receive(context.Background(), x, io.Discard); e == nil {
		t.Fatal("missing sender accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("deadline not enforced")
	}
}

func TestConfiguredMetadataSizeBoundary(t *testing.T) {
	const limit int64 = 32 << 20
	c, _ := setup(t, Config{MaxSize: limit})
	h := sha256.Sum256(nil)
	digest := hex.EncodeToString(h[:])
	for _, size := range []int64{30 << 20, limit} {
		if _, err := c.Create(context.Background(), "alice", "bob", size, digest); err != nil {
			t.Fatalf("valid metadata size %d rejected: %v", size, err)
		}
	}
	if _, err := c.Create(context.Background(), "alice", "bob", limit+1, digest); err == nil {
		t.Fatal("server accepted one byte over configured limit")
	}
}
