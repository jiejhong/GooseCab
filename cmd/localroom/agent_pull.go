package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/roomreceipt"
)

func (s *roomServer) serveAgentPull(ctx context.Context, conn *websocket.Conn, actor protocol.Actor,
	send func(any) bool, reject func(string) bool) {
	roomreceipt.Serve(ctx, conn, send, reject,
		func(read protocol.LocalRoomReadQuery) (protocol.LocalRoomReadReply, string) {
			return s.agentReadPage(actor, read)
		},
		func(ids []string) error {
			if s.commitAcknowledgement(ctx, actor, ids) != http.StatusNoContent {
				return errors.New("acknowledgement failed")
			}
			return nil
		}, nil)
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
