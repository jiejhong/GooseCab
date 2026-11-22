package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

func (s *roomServer) serveAgentPull(ctx context.Context, conn *websocket.Conn, actor protocol.Actor,
	send func(any) bool, reject func(string) bool) {
	var delivered *protocol.LocalRoomPageReceipt
	for ctx.Err() == nil {
		messageType, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var cmd agentCommand
		if messageType != websocket.TextMessage || !utf8.Valid(raw) || decodeJSON(raw, &cmd) != nil ||
			len(cmd.Event) != 0 || cmd.EventID != "" || cmd.DeliveryID != "" {
			if !reject("invalid_request") {
				return
			}
			continue
		}
		switch {
		case cmd.Type == "room.read" && cmd.Read != nil && cmd.Receipt == nil:
			delivered = nil
			reply, code := s.agentReadPage(actor, *cmd.Read)
			if code != "" {
				if !reject(code) {
					return
				}
				continue
			}
			wire, err := json.Marshal(reply)
			if err != nil || len(wire)+1 > protocol.LocalRoomPageBytes {
				if !reject("page_too_large") {
					return
				}
				continue
			}
			if !send(json.RawMessage(wire)) {
				return
			}
			delivered = reply.Receipt
		case cmd.Type == "mention.page_ack" && cmd.Receipt != nil && cmd.Read == nil:
			if delivered == nil ||
				subtle.ConstantTimeCompare([]byte(delivered.DeliveryID), []byte(cmd.Receipt.DeliveryID)) != 1 ||
				!slices.Equal(delivered.EventIDs, cmd.Receipt.EventIDs) {
				if !reject("unconfirmed_delivery") {
					return
				}
				continue
			}
			if s.commitAcknowledgement(ctx, actor, delivered.EventIDs) != http.StatusNoContent {
				if !reject("ack_failed") {
					return
				}
				continue
			}
			// Repeating the last receipt succeeds without further mutations.
			if !send(map[string]string{"type": "mention.page_acknowledged", "delivery_id": delivered.DeliveryID}) {
				return
			}
		default:
			if !reject("unsupported_message") {
				return
			}
		}
	}
}

func (s *roomServer) agentReadPage(actor protocol.Actor, read protocol.LocalRoomReadQuery) (protocol.LocalRoomReadReply, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.failed {
		return protocol.LocalRoomReadReply{}, "room_unavailable"
	}
	if read.AfterSeq < 0 || read.Limit < 1 || read.Limit > protocol.LocalRoomPageLimit {
		return protocol.LocalRoomReadReply{}, "invalid_history_range"
	}
	state := s.db.state
	start := 0
	if read.AfterCursor != "" {
		row, err := strconv.Atoi(read.AfterCursor)
		if err != nil || row < 1 || row > len(state.Events) || state.Events[row-1].Seq != read.AfterSeq {
			return protocol.LocalRoomReadReply{}, "invalid_history_cursor"
		}
		start = row
	} else {
		for start < len(state.Events) && state.Events[start].Seq <= read.AfterSeq {
			start++
		}
	}
	end := min(start+read.Limit, len(state.Events))
	page := &protocol.LocalRoomPage{
		RoomID: s.policy.room, AgentID: actor.ID, Events: make([]protocol.Event, end-start),
		NextSeq: read.AfterSeq, NextCursor: read.AfterCursor, HasMore: end < len(state.Events),
	}
	copy(page.Events, state.Events[start:end])
	reply := protocol.LocalRoomReadReply{Type: "room.read.result", Page: page}
	if len(page.Events) == 0 {
		return reply, ""
	}
	page.NextSeq = page.Events[len(page.Events)-1].Seq
	page.NextCursor = strconv.Itoa(end)
	var unread []string
	for _, event := range page.Events {
		if slices.Contains(state.Unread[event.EventID], actor) {
			unread = append(unread, event.EventID)
		}
	}
	if len(unread) != 0 {
		id, err := newDeliveryID()
		if err != nil {
			return protocol.LocalRoomReadReply{}, "room_unavailable"
		}
		reply.Receipt = &protocol.LocalRoomPageReceipt{DeliveryID: id, EventIDs: unread}
	}
	return reply, ""
}
