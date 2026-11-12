package chatmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

type Config struct{ URL, Room, AgentID, Token string }
type receipt struct {
	Status  string `json:"status"`
	EventID string `json:"event_id"`
	Room    string `json:"room_id"`
	AgentID string `json:"agent_id"`
	Seq     int64  `json:"seq"`
}
type attempt struct {
	args   SendArgs
	done   chan struct{}
	result *receipt
}
type GatewayBackend struct {
	client       *agent.Client
	config       Config
	base         *url.URL
	http         *http.Client
	mu           sync.Mutex
	attempts     map[string]*attempt
	disconnected bool
}

func validate(c Config) (*url.URL, error) {
	u, e := url.Parse(c.URL)
	if e != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/v1/agent/connect" || u.RawPath != "" {
		return nil, errors.New("expected ws(s)://host/api/v1/agent/connect without credentials, query or fragment")
	}
	if strings.TrimSpace(c.Room) == "" || len(c.Room) > 256 || strings.TrimSpace(c.AgentID) == "" || len(c.AgentID) > 128 {
		return nil, errors.New("room and agent ID required with bounded length")
	}
	ip := net.ParseIP(u.Hostname())
	if c.Token != "" && u.Scheme == "ws" && u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("remote bearer token requires wss")
	}
	return u, nil
}
func Connect(ctx context.Context, c Config) (*GatewayBackend, error) {
	u, err := validate(c)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := agent.DialWithTokenContext(dialCtx, c.URL, c.Room, c.AgentID, nil, c.Token)
	if err != nil {
		return nil, errors.New("gateway connection failed; check URL, authorization and room")
	}
	client.Conn.SetReadLimit(MaxResponseBytes)
	base := *u
	if base.Scheme == "wss" {
		base.Scheme = "https"
	} else {
		base.Scheme = "http"
	}
	b := &GatewayBackend{client: client, config: c, base: &base, attempts: map[string]*attempt{}, http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}}
	go b.pump(ctx)
	return b, nil
}
func (b *GatewayBackend) Close() error { return b.client.Close() }
func (b *GatewayBackend) pump(ctx context.Context) {
	defer func() {
		b.mu.Lock()
		b.disconnected = true
		for _, a := range b.attempts {
			if a.result == nil {
				select {
				case <-a.done:
				default:
					close(a.done)
				}
			}
		}
		b.mu.Unlock()
	}()
	for {
		msg, err := b.client.ReadContext(ctx)
		if err != nil {
			return
		}
		id, _ := msg["event_id"].(string)
		if msg["type"] == "error" && id == "" && msg["room_id"] == nil && msg["actor"] == nil && msg["seq"] == nil {
			_ = b.Close()
			return
		}
		if msg["type"] != "chat.message" || msg["room_id"] != b.config.Room {
			continue
		}
		actor, _ := msg["actor"].(map[string]any)
		payload, _ := msg["payload"].(map[string]any)
		if actor["id"] != b.config.AgentID || actor["kind"] != "agent" {
			continue
		}
		b.mu.Lock()
		a := b.attempts[id]
		cause, _ := msg["causation_id"].(string)
		if a != nil && a.result == nil && payload["text"] == a.args.Text && cause == a.args.ReplyTo {
			seq, _ := msg["seq"].(float64)
			a.result = &receipt{Status: "persisted", EventID: id, Room: b.config.Room, AgentID: b.config.AgentID, Seq: int64(seq)}
			close(a.done)
		}
		b.mu.Unlock()
	}
}
func (b *GatewayBackend) Send(ctx context.Context, a SendArgs) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if old := b.attempts[a.MessageID]; old != nil {
		if old.args != a {
			b.mu.Unlock()
			return nil, errors.New("message_id already used with different content")
		}
		if old.result != nil {
			r := *old.result
			b.mu.Unlock()
			return r, nil
		}
		b.mu.Unlock()
		return nil, errors.New("previous send is uncertain; inspect room_read before any retry")
	}
	if b.disconnected {
		b.mu.Unlock()
		return nil, errors.New("agent disconnected; restart adapter")
	}
	if len(b.attempts) >= 256 {
		b.mu.Unlock()
		return nil, errors.New("session send limit reached; inspect history and restart adapter")
	}
	item := &attempt{args: a, done: make(chan struct{})}
	b.attempts[a.MessageID] = item
	b.mu.Unlock()
	evt := protocol.Event{EventID: a.MessageID, RoomID: b.config.Room, Type: "chat.message", Actor: protocol.Actor{Kind: "agent", ID: b.config.AgentID}, TS: time.Now().UTC(), CausationID: a.ReplyTo, Payload: map[string]any{"text": a.Text}}
	if b.client.Write(map[string]any{"type": "event.publish", "event": evt}) != nil {
		return nil, errors.New("send outcome uncertain; inspect room_read")
	}
	select {
	case <-ctx.Done():
		return nil, errors.New("ack timeout or cancellation; inspect room_read before retrying")
	case <-item.done:
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if item.result == nil {
		return nil, errors.New("disconnected or rejected before acknowledgement; inspect room_read")
	}
	return *item.result, nil
}
func (b *GatewayBackend) get(ctx context.Context, path string, q url.Values) (any, error) {
	u := *b.base
	u.Path = path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid metadata request")
	}
	if b.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.config.Token)
	}
	res, err := b.http.Do(req)
	if err != nil {
		return nil, errors.New("metadata request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata request rejected (HTTP %d)", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, errors.New("metadata read failed")
	}
	if len(raw) > MaxResponseBytes {
		return nil, errors.New("metadata exceeds 256 KiB; request a smaller page")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid metadata JSON")
	}
	return v, nil
}
func (b *GatewayBackend) Read(ctx context.Context, a ReadArgs) (any, error) {
	q := url.Values{"room_id": {b.config.Room}, "after_seq": {strconv.FormatInt(a.AfterSeq, 10)}, "limit": {strconv.Itoa(a.Limit)}}
	if a.AfterCursor != "" {
		q.Set("after_cursor", a.AfterCursor)
	}
	return b.get(ctx, "/api/events", q)
}
func (b *GatewayBackend) Discover(ctx context.Context, a DiscoverArgs) (any, error) {
	q := url.Values{}
	if a.Query != "" {
		q.Set("q", a.Query)
	}
	if a.Kind != "" {
		q.Set("kind", a.Kind)
	}
	return b.get(ctx, "/api/capabilities", q)
}
