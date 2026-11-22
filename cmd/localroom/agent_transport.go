package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

const agentConnectPath = "/api/v1/agent/connect"

type agentCommand struct {
	Type       string                         `json:"type"`
	Event      json.RawMessage                `json:"event,omitempty"`
	EventID    string                         `json:"event_id,omitempty"`
	DeliveryID string                         `json:"delivery_id,omitempty"`
	Read       *protocol.LocalRoomReadQuery   `json:"read,omitempty"`
	Receipt    *protocol.LocalRoomPageReceipt `json:"receipt,omitempty"`
}

func (s *roomServer) serveAgent(w http.ResponseWriter, r *http.Request) {
	if !s.policy.agentTransport {
		http.NotFound(w, r)
		return
	}
	if len(r.Header.Values("Origin")) != 0 {
		http.Error(w, "agent transport requires a native connection", http.StatusForbidden)
		return
	}
	actor, lease, ok := s.policy.readAgent(requestAuthorization(r))
	query, err := url.ParseQuery(r.URL.RawQuery)
	rooms := query["room_id"]
	if !ok || err != nil || len(query) != 1 || len(rooms) != 1 || rooms[0] != s.policy.room {
		denyRead(w)
		return
	}
	ctx, cancel := lease.Context(r.Context())
	defer cancel()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		return len(r.Header.Values("Origin")) == 0
	}, HandshakeTimeout: 3 * time.Second}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	expiry, _ := ctx.Deadline()
	conn.SetReadLimit(maxChatBody)
	send := func(message any) bool {
		return ctx.Err() == nil && time.Now().Before(expiry) &&
			conn.SetWriteDeadline(minTime(expiry, time.Now().Add(5*time.Second))) == nil &&
			conn.WriteJSON(message) == nil
	}
	reject := func(code string) bool {
		return send(map[string]string{"type": "error", "code": code, "message": "local room agent operation rejected"})
	}
	var hello struct {
		Type         string   `json:"type"`
		AgentID      string   `json:"agent_id"`
		Capabilities []string `json:"capabilities"`
		Mode         string   `json:"mode,omitempty"`
	}
	_ = conn.SetReadDeadline(minTime(expiry, time.Now().Add(3*time.Second)))
	messageType, raw, err := conn.ReadMessage()
	if err != nil || messageType != websocket.TextMessage || !utf8.Valid(raw) || decodeJSON(raw, &hello) != nil ||
		hello.Type != "agent.hello" || hello.AgentID != actor.ID || len(hello.Capabilities) != 0 ||
		(hello.Mode != "" && hello.Mode != protocol.LocalRoomPullMode) {
		reject("invalid_hello")
		return
	}
	_ = conn.SetReadDeadline(expiry)
	if hello.Mode == protocol.LocalRoomPullMode {
		if send(map[string]string{"type": "agent.welcome", "room_id": s.policy.room, "agent_id": actor.ID, "mode": hello.Mode}) {
			s.serveAgentPull(ctx, conn, actor, send, reject)
		}
		return
	}

	s.mu.Lock()
	if s.db.failed || ctx.Err() != nil {
		s.mu.Unlock()
		reject("room_unavailable")
		return
	}
	sub := s.bus.SubscribeRoom(s.policy.room, maxEvents)
	history := slices.Clone(s.db.state.Events)
	s.mu.Unlock()
	defer s.bus.Unsubscribe(sub)
	if !send(map[string]string{"type": "agent.welcome", "room_id": s.policy.room, "agent_id": actor.ID}) {
		return
	}

	// Proofs are unique to this connection and travel only inside the
	// corresponding event frame. History IDs alone cannot clear Agent unread.
	delivered := make(map[string]string, maxEvents)
	deliver := func(event protocol.Event) bool {
		id, err := newDeliveryID()
		if err != nil {
			return false
		}
		if !send(struct {
			protocol.Event
			DeliveryID string `json:"delivery_id"`
		}{event, id}) {
			return false
		}
		delivered[event.EventID] = id
		return true
	}
	for _, event := range history {
		if !deliver(event) {
			return
		}
	}
	commands, readDone := readAgentCommands(ctx, conn)
	defer func() {
		cancel()
		_ = conn.Close()
		<-readDone
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-sub:
			if !ok || !deliver(event) {
				return
			}
		case cmd, ok := <-commands:
			if !ok {
				return
			}
			switch cmd.Type {
			case "event.publish":
				if code := s.publishAgentChat(ctx, actor, cmd); code != "" && !reject(code) {
					return
				}
			case "mention.ack":
				proof, found := delivered[cmd.EventID]
				if !found || len(cmd.Event) != 0 || cmd.Read != nil || cmd.Receipt != nil || subtle.ConstantTimeCompare([]byte(proof), []byte(cmd.DeliveryID)) != 1 {
					if !reject("unconfirmed_delivery") {
						return
					}
					continue
				}
				if status := s.commitAcknowledgement(ctx, actor, []string{cmd.EventID}); status != http.StatusNoContent {
					if !reject("ack_failed") {
						return
					}
					continue
				}
				if !send(map[string]string{"type": "mention.acknowledged", "event_id": cmd.EventID}) {
					return
				}
			default:
				if !reject("unsupported_message") {
					return
				}
			}
		}
	}
}

func readAgentCommands(ctx context.Context, conn *websocket.Conn) (<-chan agentCommand, <-chan struct{}) {
	commands, done := make(chan agentCommand), make(chan struct{})
	go func() {
		defer close(done)
		defer close(commands)
		for {
			messageType, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var cmd agentCommand
			if messageType != websocket.TextMessage || !utf8.Valid(raw) || decodeJSON(raw, &cmd) != nil {
				cmd = agentCommand{}
			}
			select {
			case commands <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}()
	return commands, done
}

func (s *roomServer) publishAgentChat(ctx context.Context, actor protocol.Actor, cmd agentCommand) string {
	if !s.policy.members[actor].Write {
		return "write_denied"
	}
	var event struct {
		Type    string          `json:"type"`
		EventID string          `json:"event_id"`
		Seq     int64           `json:"seq"`
		RoomID  string          `json:"room_id"`
		Actor   protocol.Actor  `json:"actor"`
		TS      time.Time       `json:"ts"`
		Payload json.RawMessage `json:"payload"`
	}
	var chat chatSubmission
	if len(cmd.Event) == 0 || cmd.EventID != "" || cmd.DeliveryID != "" || cmd.Read != nil || cmd.Receipt != nil || decodeJSON(cmd.Event, &event) != nil ||
		event.Type != "chat.message" || event.EventID != "" || event.Seq != 0 || event.RoomID != s.policy.room || event.Actor != actor ||
		decodeJSON(event.Payload, &chat) != nil || !chat.valid() || chat.MentionAll {
		return "invalid_chat"
	}
	if _, status := s.commitMessage(ctx, actor, chat); status != http.StatusCreated {
		return "write_failed"
	}
	return ""
}

func newDeliveryID() (string, error) {
	var proof [16]byte
	if _, err := rand.Read(proof[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(proof[:]), nil
}
