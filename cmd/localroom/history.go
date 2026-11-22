package main

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

const (
	historyPageSize     = 20
	historyCursorPrefix = "h1."
)

type roomHistoryPage struct {
	RoomID       string           `json:"room_id"`
	Actor        protocol.Actor   `json:"actor"`
	DisplayName  string           `json:"display_name"`
	CanWrite     bool             `json:"can_write"`
	CanBroadcast bool             `json:"can_broadcast"`
	Events       []protocol.Event `json:"events"`
	NextBefore   string           `json:"next_before,omitempty"`
}

func (s *roomServer) serveHistory(ctx context.Context, w http.ResponseWriter, r *http.Request, actor protocol.Actor, canWrite bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	var before string
	if r.URL.RawQuery != "" {
		values := query["before"]
		if err != nil || len(query) != 1 || len(values) != 1 {
			http.Error(w, "invalid history cursor", http.StatusBadRequest)
			return
		}
		var ok bool
		before, ok = strings.CutPrefix(values[0], historyCursorPrefix)
		if !ok || before == "" || len(before) > 128 {
			http.Error(w, "invalid history cursor", http.StatusBadRequest)
			return
		}
	}
	events, next, status := s.historyPage(before)
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	writeLocalJSON(ctx, w, status, roomHistoryPage{
		RoomID: s.policy.room, Actor: publicRoomActor(actor), DisplayName: s.policy.displayName(actor),
		CanWrite: canWrite, CanBroadcast: canWrite && s.policy.canBroadcast(actor),
		Events: events, NextBefore: next,
	})
}

func (s *roomServer) historyPage(before string) ([]protocol.Event, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.failed {
		return nil, "", http.StatusServiceUnavailable
	}
	history := s.db.state.Events
	end := len(history)
	if before != "" {
		end = slices.IndexFunc(history, func(event protocol.Event) bool { return event.EventID == before })
		if end < 0 {
			return nil, "", http.StatusBadRequest
		}
	}
	start := max(0, end-historyPageSize)
	events := make([]protocol.Event, end-start)
	copy(events, history[start:end])
	slices.Reverse(events)
	var next string
	if start > 0 {
		next = historyCursorPrefix + history[start].EventID
	}
	return events, next, http.StatusOK
}
