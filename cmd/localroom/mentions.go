package main

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

const mentionPageSize = 20

type mentionPage struct {
	UnreadCount int              `json:"unread_count"`
	Events      []protocol.Event `json:"events"`
	NextBefore  string           `json:"next_before,omitempty"`
}

func (s *roomServer) serveMentions(ctx context.Context, w http.ResponseWriter, r *http.Request, actor protocol.Actor) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	var before string
	if len(query) != 0 {
		values := query["before"]
		if len(query) != 1 || len(values) != 1 || values[0] == "" || len(values[0]) > 128 {
			http.Error(w, "invalid mention page", http.StatusBadRequest)
			return
		}
		before = values[0]
	}
	if err != nil || (r.URL.RawQuery != "" && before == "") {
		http.Error(w, "invalid mention page", http.StatusBadRequest)
		return
	}
	page, status := s.mentionPage(actor, before)
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	writeLocalJSON(ctx, w, status, page)
}

func (s *roomServer) mentionPage(actor protocol.Actor, before string) (mentionPage, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.failed {
		return mentionPage{}, http.StatusServiceUnavailable
	}
	state := s.db.state
	end := len(state.Events)
	if before != "" {
		end = slices.IndexFunc(state.Events, func(event protocol.Event) bool {
			return event.EventID == before
		})
		if end < 0 {
			return mentionPage{}, http.StatusBadRequest
		}
	}
	page := mentionPage{Events: make([]protocol.Event, 0, mentionPageSize)}
	for i := len(state.Events) - 1; i >= 0; i-- {
		event := state.Events[i]
		if !slices.Contains(state.Unread[event.EventID], actor) {
			continue
		}
		page.UnreadCount++
		if i >= end {
			continue
		}
		if len(page.Events) < mentionPageSize {
			page.Events = append(page.Events, event)
		} else {
			page.NextBefore = page.Events[mentionPageSize-1].EventID
		}
	}
	return page, http.StatusOK
}

func (s *roomServer) acknowledgeMentions(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	actor, lease, ok := s.policy.readHuman(requestAuthorization(r))
	if !ok {
		denyRead(w)
		return
	}
	ctx, cancel := lease.Context(r.Context())
	defer cancel()
	var request struct {
		EventIDs []string `json:"event_ids"`
	}
	if status := readJSONBody(w, r, 4<<10, &request); status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	if len(request.EventIDs) == 0 || len(request.EventIDs) > mentionPageSize {
		http.Error(w, "invalid event list", http.StatusBadRequest)
		return
	}
	for _, id := range request.EventIDs {
		if id == "" || len(id) > 128 {
			http.Error(w, "invalid event ID", http.StatusBadRequest)
			return
		}
	}
	status := s.commitAcknowledgement(ctx, actor, request.EventIDs)
	if status == http.StatusUnauthorized {
		denyRead(w)
		return
	}
	if status != http.StatusNoContent {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.WriteHeader(status)
}

func (s *roomServer) commitAcknowledgement(ctx context.Context, actor protocol.Actor, ids []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline, _ := ctx.Deadline()
	if ctx.Err() != nil || !time.Now().Before(deadline) {
		return http.StatusUnauthorized
	}
	if s.db.failed || s.db.acknowledge(actor, ids) != nil {
		return http.StatusServiceUnavailable
	}
	return http.StatusNoContent
}
