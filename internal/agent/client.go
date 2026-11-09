package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// DefaultReadTimeout bounds an individual event read. Callers that need to
	// wait for a sequence of events should use their own overall deadline and
	// pass the remaining duration to ReadWithTimeout.
	DefaultReadTimeout = 5 * time.Second
	dialReadTimeout    = 2 * time.Second
)

// Client is a minimal outbound Agent Node connection. It never exposes an
// inbound port.
type Client struct {
	Conn    *websocket.Conn
	AgentID string
	RoomID  string

	pendingMu sync.Mutex
	pending   []map[string]any
}

// Dial establishes an agent connection and waits for the welcome message. A
// gateway may replay room events before processing the new hello, so those
// frames are retained for the first Read call instead of being discarded.
func Dial(url, room, id string, caps []string) (*Client, error) {
	c, _, err := websocket.DefaultDialer.Dial(url+"?room_id="+room, nil)
	if err != nil {
		return nil, err
	}
	x := &Client{Conn: c, AgentID: id, RoomID: room}
	if err = x.Write(map[string]any{"type": "agent.hello", "agent_id": id, "capabilities": caps}); err != nil {
		_ = c.Close()
		return nil, err
	}

	deadline := time.Now().Add(dialReadTimeout)
	for {
		if err = c.SetReadDeadline(deadline); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("set agent welcome deadline: %w", err)
		}
		var msg map[string]any
		if err = c.ReadJSON(&msg); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("agent welcome: %w", err)
		}
		if typ, _ := msg["type"].(string); typ == "agent.welcome" {
			break
		}
		x.queue(msg)
	}
	if err = c.SetReadDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("clear agent welcome deadline: %w", err)
	}
	return x, nil
}

func (c *Client) Write(v any) error { return c.Conn.WriteJSON(v) }

// Read reads one event with a bounded default deadline. It never blocks
// indefinitely when the expected event was not published.
func (c *Client) Read() (map[string]any, error) {
	return c.ReadWithTimeout(DefaultReadTimeout)
}

// ReadWithTimeout reads one event and bounds the websocket read by timeout.
// The deadline is cleared only after a successful read; after an error the
// connection remains timed out instead of allowing a later call to hang.
func (c *Client) ReadWithTimeout(timeout time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("agent read timeout must be positive")
	}
	if msg, ok := c.dequeue(); ok {
		return msg, nil
	}
	if err := c.Conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("set agent read deadline: %w", err)
	}
	var msg map[string]any
	if err := c.Conn.ReadJSON(&msg); err != nil {
		return nil, fmt.Errorf("read agent event: %w", err)
	}
	if err := c.Conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear agent read deadline: %w", err)
	}
	return msg, nil
}

func (c *Client) queue(msg map[string]any) {
	c.pendingMu.Lock()
	c.pending = append(c.pending, msg)
	c.pendingMu.Unlock()
}

func (c *Client) dequeue() (map[string]any, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if len(c.pending) == 0 {
		return nil, false
	}
	msg := c.pending[0]
	c.pending[0] = nil
	c.pending = c.pending[1:]
	return msg, true
}

func (c *Client) Close() error { return c.Conn.Close() }

// CopyLocal is the data-plane primitive used by direct or relay
// implementations. The gateway sees only metadata.
func CopyLocal(src, dst string) (size int64, sha string, err error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// CopyWithMode validates the selected signaling path; bytes still stay local
// to the Agents.
func CopyWithMode(mode, src, dst string) (int64, string, error) {
	if mode != "direct" && mode != "relay" {
		return 0, "", fmt.Errorf("unsupported transfer mode: %s", mode)
	}
	return CopyLocal(src, dst)
}
