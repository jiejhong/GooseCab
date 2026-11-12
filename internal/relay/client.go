package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	URL   string
	Token string
}

func (c Client) endpoint(path string) (string, error) {
	u, e := url.Parse(c.URL)
	if e != nil {
		return "", e
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("relay URL must use http or https")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid gateway URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}
func (c Client) Create(ctx context.Context, sender, recipient string, size int64, sha string) (Ticket, error) {
	t := Ticket{Sender: sender, Recipient: recipient, Size: size, SHA256: sha}
	if !valid(t, DefaultMaxSize) {
		return Ticket{}, errors.New("invalid transfer metadata")
	}
	endpoint, e := c.endpoint("/api/v1/relay")
	if e != nil {
		return Ticket{}, e
	}
	b, _ := json.Marshal(t)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if e != nil {
		return Ticket{}, e
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return Ticket{}, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return Ticket{}, fmt.Errorf("create relay: HTTP %d", resp.StatusCode)
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&t); e != nil {
		return Ticket{}, e
	}
	if !valid(t, DefaultMaxSize) || len(t.ID) != 64 || len(t.SendSecret) != 64 || len(t.ReceiveSecret) != 64 {
		return Ticket{}, errors.New("invalid relay ticket")
	}
	return t, nil
}
func (c Client) connect(ctx context.Context, t Ticket, role string) (*websocket.Conn, func(), error) {
	if !valid(t, DefaultMaxSize) {
		return nil, nil, errors.New("invalid ticket")
	}
	endpoint, e := c.endpoint("/api/v1/relay/connect")
	if e != nil {
		return nil, nil, e
	}
	u, _ := url.Parse(endpoint)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	h := http.Header{}
	h.Set("X-Relay-ID", t.ID)
	h.Set("X-Relay-Role", role)
	if role == "send" {
		h.Set("X-Relay-Agent", t.Sender)
		h.Set("X-Relay-Secret", t.SendSecret)
	} else {
		h.Set("X-Relay-Agent", t.Recipient)
		h.Set("X-Relay-Secret", t.ReceiveSecret)
	}
	if c.Token != "" {
		h.Set("Authorization", "Bearer "+c.Token)
	}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second, ReadBufferSize: ChunkSize, WriteBufferSize: ChunkSize}
	conn, resp, e := d.DialContext(ctx, u.String(), h)
	if e != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, nil, e
	}
	deadline := time.Now().Add(2 * time.Minute)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	conn.SetReadLimit(ChunkSize)
	_ = conn.SetReadDeadline(deadline)
	_ = conn.SetWriteDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cleanup := func() { stop(); _ = conn.Close() }
	if e = expect(conn, "ready"); e != nil {
		cleanup()
		return nil, nil, e
	}
	return conn, cleanup, nil
}
func (c Client) Send(ctx context.Context, t Ticket, src io.Reader) error {
	conn, cleanup, e := c.connect(ctx, t, "send")
	if e != nil {
		return e
	}
	defer cleanup()
	buf := make([]byte, ChunkSize)
	var sent int64
	for sent < t.Size {
		want := int64(len(buf))
		if t.Size-sent < want {
			want = t.Size - sent
		}
		n, e := io.ReadFull(src, buf[:want])
		if e != nil {
			return e
		}
		if e = conn.WriteMessage(websocket.BinaryMessage, buf[:n]); e != nil {
			return e
		}
		sent += int64(n)
	}
	// One extra byte detects an incorrect caller size without buffering the source.
	var extra [1]byte
	if n, e := io.ReadFull(src, extra[:]); n != 0 || e != io.EOF {
		return errors.New("source exceeds declared size")
	}
	if e = conn.WriteMessage(websocket.TextMessage, []byte("done")); e != nil {
		return e
	}
	return expect(conn, "ok")
}

// Receive validates bytes before acknowledgement. dst must be a private staging
// writer: bytes may have been written when this method returns an error.
func (c Client) Receive(ctx context.Context, t Ticket, dst io.Writer) error {
	return c.receive(ctx, t, dst, nil)
}

// ReceiveAndFinalize acknowledges only after verified staging bytes and the local
// finalizer both succeed. finalize must be bounded and must not read remote paths.
func (c Client) ReceiveAndFinalize(ctx context.Context, t Ticket, dst io.Writer, finalize func() error) error {
	return c.receive(ctx, t, dst, finalize)
}
func (c Client) receive(ctx context.Context, t Ticket, dst io.Writer, finalize func() error) error {
	conn, cleanup, e := c.connect(ctx, t, "receive")
	if e != nil {
		return e
	}
	defer cleanup()
	h := sha256.New()
	var got int64
	for got < t.Size {
		typ, b, e := conn.ReadMessage()
		if e != nil {
			return e
		}
		if typ != websocket.BinaryMessage || len(b) == 0 || int64(len(b)) > t.Size-got {
			return errors.New("invalid data frame")
		}
		n, e := dst.Write(b)
		if e != nil {
			return e
		}
		if n != len(b) {
			return io.ErrShortWrite
		}
		_, _ = h.Write(b)
		got += int64(len(b))
	}
	if e = expect(conn, "done"); e != nil {
		return e
	}
	if hex.EncodeToString(h.Sum(nil)) != t.SHA256 {
		return errors.New("SHA256 mismatch")
	}
	if finalize != nil {
		if e := finalize(); e != nil {
			return e
		}
	}
	return conn.WriteMessage(websocket.TextMessage, []byte("ok"))
}

// ReceiveFile confines installation to an existing, agent-owned trusted directory.
// basename is chosen locally, never by a remote peer. Hard-link publication is
// atomic and refuses overwrite. Protect directory ancestors from untrusted writes.
// File data is synced everywhere; Windows cannot sync the directory entry, so
// successful publication there does not guarantee metadata survives power loss.
func (c Client) ReceiveFile(ctx context.Context, t Ticket, directory, basename string) (string, error) {
	if basename == "" || basename == "." || basename == ".." || filepath.Base(basename) != basename || strings.ContainsAny(basename, "/\\") {
		return "", errors.New("destination must be a plain basename")
	}
	dir, e := filepath.EvalSymlinks(directory)
	if e != nil {
		return "", e
	}
	f, e := os.CreateTemp(dir, ".relay-*")
	if e != nil {
		return "", e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	dest := filepath.Join(dir, basename)
	if e = c.receive(ctx, t, f, func() error {
		if err := f.Sync(); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Link(f.Name(), dest); err != nil {
			return err
		}
		return syncPublicationDirectory(dir)
	}); e != nil {
		return "", e
	}
	return dest, nil
}
