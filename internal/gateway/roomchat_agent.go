package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/roomreceipt"
)

func (c *RoomChat) serveAgentRead(ctx context.Context, w http.ResponseWriter, r *http.Request, room string, actor protocol.Actor, token string, settings roomChatGrants) {
	upgrader := websocket.Upgrader{HandshakeTimeout: 3 * time.Second, CheckOrigin: func(r *http.Request) bool { return len(r.Header.Values("Origin")) == 0 }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	expires, _ := ctx.Deadline()
	send := func(v any) bool {
		ok := ctx.Err() == nil && time.Now().Before(expires) &&
			conn.SetWriteDeadline(earlierRoomChatTime(expires, time.Now().Add(5*time.Second))) == nil && conn.WriteJSON(v) == nil
		// Only successful read replies are passed as pre-encoded JSON by roomreceipt.
		if _, page := v.(json.RawMessage); ok && page {
			c.activity.record(ctx, room, actor)
		}
		return ok
	}
	reject := func(code string) bool {
		return send(map[string]string{"type": "error", "code": code, "message": "room chat operation rejected"})
	}
	conn.SetReadLimit(32 << 10)
	if conn.SetReadDeadline(earlierRoomChatTime(expires, time.Now().Add(3*time.Second))) != nil {
		return
	}
	kind, raw, err := conn.ReadMessage()
	var hello struct {
		Type    string `json:"type"`
		AgentID string `json:"agent_id"`
		Mode    string `json:"mode"`
	}
	if err != nil || kind != websocket.TextMessage || decodeRoomChatJSON(bytes.NewReader(raw), &hello) != nil ||
		hello.Type != "agent.hello" || hello.AgentID != actor.ID || hello.Mode != protocol.LocalRoomPullMode {
		reject("invalid_hello")
		return
	}
	if conn.SetReadDeadline(expires) != nil ||
		!send(map[string]string{"type": "agent.welcome", "agent_id": actor.ID, "room_id": room, "mode": protocol.LocalRoomPullMode}) {
		return
	}
	roomreceipt.Serve(ctx, conn, send, reject,
		func(query protocol.LocalRoomReadQuery) (protocol.LocalRoomReadReply, string) {
			request, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return c.agentPage(request, room, actor, token, query, settings)
		},
		func(ids []string) error {
			request, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return c.ws.Store.AcknowledgeRoomChat(request, room, actor, ids)
		})
}

func earlierRoomChatTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (c *RoomChat) agentPage(ctx context.Context, room string, actor protocol.Actor, token string, query protocol.LocalRoomReadQuery, settings roomChatGrants) (protocol.LocalRoomReadReply, string) {
	if query.AfterSeq < 0 || query.Limit < 1 || query.Limit > protocol.LocalRoomPageLimit {
		return protocol.LocalRoomReadReply{}, "invalid_history_range"
	}
	if query.AfterCursor != "" {
		if row, err := strconv.ParseInt(query.AfterCursor, 10, 64); err != nil || row < 1 {
			return protocol.LocalRoomReadReply{}, "invalid_history_cursor"
		}
	}
	stored, err := c.ws.Store.History(ctx, room, query.AfterSeq, query.AfterCursor, nil, nil, query.Limit)
	if err != nil {
		return protocol.LocalRoomReadReply{}, "room_unavailable"
	}
	page := &protocol.LocalRoomPage{RoomID: room, AgentID: actor.ID, Events: make([]protocol.Event, 0, len(stored.Events)),
		NextSeq: query.AfterSeq, NextCursor: query.AfterCursor, HasMore: stored.HasMore}
	if len(stored.Events) > 0 {
		page.NextSeq = stored.Events[len(stored.Events)-1].Seq
		page.NextCursor = stored.NextCursor
	}
	redact := newHistoryRedactor(c.legacy, token)
	eligible := make([]protocol.Event, 0, len(stored.Events))
	for _, event := range stored.Events {
		safe, err := redact.event(event)
		if err != nil {
			return protocol.LocalRoomReadReply{}, "room_unavailable"
		}
		page.Events = append(page.Events, safe)
		if safe.EventID == event.EventID && safe.RoomID == room {
			eligible = append(eligible, event)
		}
	}
	reply := protocol.LocalRoomReadReply{Type: "room.read.result", Page: page}
	if settings.readOnly && !settings.legacyAudience {
		return reply, ""
	}
	ids, err := c.ws.Store.RoomChatPending(ctx, room, actor, eligible)
	if err != nil {
		return protocol.LocalRoomReadReply{}, "room_unavailable"
	}
	if settings.legacyAudience {
		page.PrivateMentionEventIDs = ids
	}
	if len(ids) > 0 && !settings.readOnly {
		reply.Receipt = &protocol.LocalRoomPageReceipt{EventIDs: ids}
	}
	return reply, ""
}
