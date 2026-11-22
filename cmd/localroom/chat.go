package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

const (
	maxChatText  = 4096
	maxChatBody  = 32 << 10
	chatInterval = 500 * time.Millisecond
)

type chatSubmission struct {
	Text       string          `json:"text"`
	Mention    *protocol.Actor `json:"mention,omitempty"`
	MentionAll bool            `json:"mention_all,omitempty"`
}

type localChat struct {
	chatSubmission
	AuthorDisplayName string `json:"author_display_name,omitempty"`
}

func (chat chatSubmission) valid() bool {
	return strings.TrimSpace(chat.Text) != "" && len(chat.Text) <= maxChatText &&
		!strings.ContainsRune(chat.Text, 0) && !(chat.Mention != nil && chat.MentionAll)
}

func (s *roomServer) postMessage(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	auth := requestAuthorization(r)
	actor, lease, ok := s.policy.readHuman(auth)
	if !ok {
		denyRead(w)
		return
	}
	if writer, ok := s.policy.writer(auth); !ok || writer != actor {
		http.Error(w, "room write denied", http.StatusForbidden)
		return
	}
	ctx, cancel := lease.Context(r.Context())
	defer cancel()

	var chat chatSubmission
	if status := readJSONBody(w, r, maxChatBody, &chat); status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	if !chat.valid() {
		http.Error(w, "invalid message", http.StatusBadRequest)
		return
	}

	event, status := s.commitMessage(ctx, actor, chat)
	if status == http.StatusUnauthorized {
		denyRead(w)
		return
	}
	if status != http.StatusCreated {
		if status == http.StatusConflict {
			http.Error(w, "room history is full", status)
			return
		}
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	writeLocalJSON(ctx, w, status, event)
}

func (s *roomServer) commitMessage(ctx context.Context, actor protocol.Actor, chat chatSubmission) (protocol.Event, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if deadline, ok := ctx.Deadline(); !ok || !now.Before(deadline) || ctx.Err() != nil {
		return protocol.Event{}, http.StatusUnauthorized
	}
	var recipients []protocol.Actor
	switch {
	case chat.MentionAll:
		if !s.policy.canBroadcast(actor) {
			return protocol.Event{}, http.StatusForbidden
		}
		recipients = s.policy.broadcastRecipients(actor, now)
	case chat.Mention != nil:
		if !s.policy.canMention(*chat.Mention, now) {
			return protocol.Event{}, http.StatusBadRequest
		}
		recipients = []protocol.Actor{*chat.Mention}
	}
	if now.Sub(s.lastWrite[actor]) < chatInterval {
		return protocol.Event{}, http.StatusTooManyRequests
	}

	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return protocol.Event{}, http.StatusServiceUnavailable
	}
	event, err := s.db.append(protocol.Event{
		RoomID: s.policy.room, EventID: hex.EncodeToString(id[:]), Type: "chat.message",
		Actor: publicRoomActor(actor), TS: now.UTC(), Payload: localChat{chatSubmission: chat, AuthorDisplayName: s.policy.displayName(actor)},
	}, recipients)
	if errors.Is(err, errRoomFull) {
		return protocol.Event{}, http.StatusConflict
	}
	if err != nil {
		return protocol.Event{}, http.StatusServiceUnavailable
	}
	s.lastWrite[actor] = now
	s.bus.Publish(event)
	return event, http.StatusCreated
}
