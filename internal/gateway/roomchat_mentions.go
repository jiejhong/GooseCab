package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

const mentionProofLifetime = 90 * time.Second

func (c *RoomChat) appendTrackedMention(ctx context.Context, event *protocol.Event, recipients []protocol.Actor, replyTo string) error {
	c.ws.eventMu.Lock()
	defer c.ws.eventMu.Unlock()
	if err := c.ws.Store.AppendTrackedRoomChat(ctx, event, recipients, replyTo); err != nil {
		return err
	}
	c.ws.Bus.Publish(*event)
	return nil
}

func (c *RoomChat) mentionProof(room string, actor protocol.Actor, id string, deadline int64) []byte {
	scope, _ := json.Marshal([]string{room, actor.Kind, actor.ID, id})
	expiry := binary.BigEndian.AppendUint64(nil, uint64(deadline))
	tag := hmac.New(sha256.New, c.mentionKey[:])
	_, _ = tag.Write(expiry)
	_, _ = tag.Write(scope)
	return tag.Sum(expiry)
}

func (c *RoomChat) validMentionProof(proof, room string, actor protocol.Actor, id string, now time.Time) bool {
	raw, err := base64.RawURLEncoding.DecodeString(proof)
	if err != nil || len(raw) != 8+sha256.Size {
		return false
	}
	expiry := int64(binary.BigEndian.Uint64(raw[:8]))
	return expiry > now.Unix() && hmac.Equal(raw, c.mentionProof(room, actor, id, expiry))
}

func (c *RoomChat) serveMentionHTTP(w http.ResponseWriter, r *http.Request, room string, actor protocol.Actor, token string) {
	redact := newHistoryRedactor(c.legacy, token)
	switch r.URL.Path {
	case "/api/room-chat/mentions":
		events, err := c.ws.Store.PendingMentions(r.Context(), room, actor, 16)
		if err != nil {
			http.Error(w, "could not read room mentions", http.StatusServiceUnavailable)
			return
		}
		type notice struct {
			Event protocol.Event `json:"event"`
			Proof string         `json:"proof"`
		}
		items := make([]notice, 0, len(events))
		expiry := time.Now().Add(mentionProofLifetime).Unix()
		for _, e := range events {
			safe, err := redact.event(e)
			if err != nil || safe.EventID != e.EventID {
				http.Error(w, "could not prepare room mentions", http.StatusInternalServerError)
				return
			}
			proof := base64.RawURLEncoding.EncodeToString(c.mentionProof(room, actor, e.EventID, expiry))
			items = append(items, notice{Event: safe, Proof: proof})
		}
		writeRoomChatJSON(w, http.StatusOK, map[string]any{"items": items})
	case "/api/room-chat/mentions/status":
		id := r.URL.Query().Get("event_id")
		if !protocol.ValidMentionEventID(id) {
			http.Error(w, "invalid mention event ID", http.StatusBadRequest)
			return
		}
		statuses, err := c.ws.Store.MentionStatuses(r.Context(), room, actor, id)
		if err != nil {
			http.Error(w, "could not read room mention status", http.StatusServiceUnavailable)
			return
		}
		for i := range statuses {
			statuses[i].Recipient.ID = redact.text(statuses[i].Recipient.ID)
		}
		writeRoomChatJSON(w, http.StatusOK, map[string]any{"recipients": statuses})
	case "/api/room-chat/mentions/ack":
		var input struct {
			EventID string `json:"event_id"`
			Proof   string `json:"proof"`
		}
		if !readRoomChatBody(w, r, 4096, &input) {
			return
		}
		if !protocol.ValidMentionEventID(input.EventID) || !c.validMentionProof(input.Proof, room, actor, input.EventID, time.Now()) {
			http.Error(w, "mention delivery not confirmed", http.StatusForbidden)
			return
		}
		if err := c.ws.Store.ConfirmMention(r.Context(), room, actor, input.EventID); err != nil {
			http.Error(w, "could not confirm mention delivery", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (c *RoomChat) nextAgentMention(ctx context.Context, room string, actor protocol.Actor, token string) (protocol.LocalRoomReadReply, string) {
	sub := c.ws.Bus.SubscribeRoom(room, 8)
	defer c.ws.Bus.Unsubscribe(sub)
	wait := time.NewTimer(2 * time.Second)
	defer wait.Stop()
	redact := newHistoryRedactor(c.legacy, token)
	page := &protocol.LocalRoomPage{RoomID: room, AgentID: actor.ID, Events: []protocol.Event{}}
	reply := protocol.LocalRoomReadReply{Type: "mention.next.result", Page: page}
	for {
		events, err := c.ws.Store.PendingMentions(ctx, room, actor, 1)
		if err != nil {
			return protocol.LocalRoomReadReply{}, "room_unavailable"
		}
		if len(events) != 0 {
			safe, err := redact.event(events[0])
			if err != nil || safe.EventID != events[0].EventID {
				return protocol.LocalRoomReadReply{}, "room_unavailable"
			}
			page.Events = append(page.Events, safe)
			reply.Receipt = &protocol.LocalRoomPageReceipt{EventIDs: []string{safe.EventID}}
			return reply, ""
		}
		select {
		case _, open := <-sub:
			if !open {
				return protocol.LocalRoomReadReply{}, "room_unavailable"
			}
		case <-ctx.Done():
			return protocol.LocalRoomReadReply{}, "room_unavailable"
		case <-wait.C:
			return reply, ""
		}
	}
}

func (c *RoomChat) agentMentionReply(ctx context.Context, room string, actor protocol.Actor, token string, input protocol.MentionReply) (protocol.Event, string) {
	id, err := uuid.Parse(input.MessageID)
	if !protocol.ValidMentionEventID(input.EventID) || err != nil || id.Version() != 4 || id.String() != input.MessageID ||
		!validRoomChatText(input.Text, c.legacy, token) {
		return protocol.Event{}, "invalid_mention_reply"
	}
	event := protocol.Event{RoomID: room, Actor: actor, EventID: input.MessageID, Type: "chat.message",
		Payload: struct {
			Text string `json:"text"`
		}{input.Text}}
	if err := c.appendTrackedMention(ctx, &event, nil, input.EventID); err != nil {
		return protocol.Event{}, "mention_reply_not_committed"
	}
	return event, ""
}
