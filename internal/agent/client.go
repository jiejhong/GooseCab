package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

const (
	// DefaultReadTimeout bounds an individual event read. Callers that need to
	// wait for a sequence of events should use their own overall deadline and
	// pass the remaining duration to ReadWithTimeout.
	DefaultReadTimeout = 5 * time.Second
	// Room replay precedes welcome. Allow bounded large-history recovery under
	// load; DialWithTokenContext still honors a caller's shorter deadline.
	dialReadTimeout = 10 * time.Second
)

// Client is a minimal outbound Agent Node connection. It never exposes an
// inbound port.
type Client struct {
	Conn    *websocket.Conn
	AgentID string
	RoomID  string

	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   []map[string]any
}

// Dial establishes an agent connection and waits for the welcome message. A
// gateway may replay room events before processing the new hello, so those
// frames are retained for the first Read call instead of being discarded.
func Dial(url, room, id string, caps []string) (*Client, error) {
	return dial(context.Background(), url, room, id, caps, "")
}

// DialWithToken is the authenticated variant of Dial. The token is sent only
// as a Bearer Authorization header during the websocket handshake; it is not
// included in protocol messages or persisted by the client.
func DialWithToken(url, room, id string, caps []string, token string) (*Client, error) {
	return dial(context.Background(), url, room, id, caps, token)
}

// DialWithTokenContext applies cancellation to TCP/TLS/websocket handshake and
// pre-welcome replay, not just to reads after a connection has been established.
func DialWithTokenContext(ctx context.Context, url, room, id string, caps []string, token string) (*Client, error) {
	return dial(ctx, url, room, id, caps, token)
}

func dial(ctx context.Context, url, room, id string, caps []string, token string) (*Client, error) {
	endpoint, err := urlpkg(url, room)
	if err != nil {
		return nil, err
	}
	var headers http.Header
	if token != "" {
		headers = make(http.Header)
		headers.Set("Authorization", "Bearer "+token)
	}
	c, response, err := websocket.DefaultDialer.DialContext(ctx, endpoint, headers)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	x := &Client{Conn: c, AgentID: id, RoomID: room}
	if err = x.Write(map[string]any{"type": "agent.hello", "agent_id": id, "capabilities": caps}); err != nil {
		_ = c.Close()
		return nil, err
	}

	deadline := time.Now().Add(dialReadTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
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
	if err = ctx.Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return x, nil
}

// urlpkg adds room_id without discarding query parameters supplied by a
// reverse proxy or deployment. It also escapes room IDs containing spaces or
// other reserved characters.
func urlpkg(raw, room string) (string, error) {
	u, err := neturl.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse gateway URL: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return "", fmt.Errorf("gateway URL must use ws or wss scheme")
	}
	if u.Host == "" {
		return "", fmt.Errorf("gateway URL must include a host")
	}
	query := u.Query()
	query.Set("room_id", room)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (c *Client) Write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.Conn.SetWriteDeadline(time.Now().Add(DefaultReadTimeout)); err != nil {
		return err
	}
	return c.Conn.WriteJSON(v)
}

// ReadContext is for long-lived readers. Cancellation closes the connection;
// unlike a websocket timeout it cannot leave a reader spinning on a poisoned socket.
func (c *Client) ReadContext(ctx context.Context) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if msg, ok := c.dequeue(); ok {
		return msg, nil
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	var msg map[string]any
	if err := c.Conn.ReadJSON(&msg); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("read agent event: %w", err)
	}
	return msg, nil
}

// SendChat publishes a room chat message through the event stream. Chat text
// is metadata only; the gateway does not execute or interpret it.
func (c *Client) SendChat(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("chat text must not be empty")
	}
	return c.SendEvent("chat.message", map[string]any{
		"text": text,
	})
}

// SendEvent publishes a room event while keeping the event's room and actor
// bound to this client connection.
func (c *Client) SendEvent(eventType string, payload any) error {
	if strings.TrimSpace(eventType) == "" {
		return fmt.Errorf("event type must not be empty")
	}
	return c.Write(map[string]any{
		"type": "event.publish",
		"event": protocol.Event{
			RoomID:  c.RoomID,
			Type:    eventType,
			Actor:   protocol.Actor{Kind: "agent", ID: c.AgentID},
			TS:      time.Now().UTC(),
			Payload: payload,
		},
	})
}

// Invoke asks a target agent to execute a capability. Progress and result
// frames are received through Read/ReadWithTimeout like every other event.
func (c *Client) Invoke(requestID, capabilityID, targetAgentID string, payload any) error {
	if strings.TrimSpace(requestID) == "" {
		return fmt.Errorf("request ID must not be empty")
	}
	if strings.TrimSpace(capabilityID) == "" {
		return fmt.Errorf("capability ID must not be empty")
	}
	msg := map[string]any{
		"type":          "capability.invoke",
		"request_id":    requestID,
		"capability_id": capabilityID,
		"payload":       payload,
	}
	if strings.TrimSpace(targetAgentID) != "" {
		msg["target_agent_id"] = targetAgentID
	}
	return c.Write(msg)
}

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

// CopyLocal is a legacy same-process filesystem helper, not a cross-machine
// data plane. Real network transfers use internal/relay.
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

// CopyWithMode is a legacy compatibility wrapper around CopyLocal. Its mode
// argument does not create a network transfer; use internal/relay instead.
func CopyWithMode(mode, src, dst string) (int64, string, error) {
	if mode != "direct" && mode != "relay" {
		return 0, "", fmt.Errorf("unsupported transfer mode: %s", mode)
	}
	return CopyLocal(src, dst)
}

// Persisted room events may legitimately use the type "error". Only the
// unwrapped control frame is a protocol rejection, including during replay.
func isProtocolError(msg map[string]any) bool {
	_, hasEventID := msg["event_id"]
	_, hasRoom := msg["room_id"]
	_, hasSeq := msg["seq"]
	_, hasActor := msg["actor"]
	code, _ := msg["code"].(string)
	return msg["type"] == "error" && code != "" && !hasEventID && !hasRoom && !hasSeq && !hasActor
}
