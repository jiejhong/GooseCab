package gateway

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

// postAgentRoomChat is called only from an authenticated, opted-in room
// connection after the server has bound the room and author.
func (c *RoomChat) postAgentRoomChat(ctx context.Context, event *protocol.Event, text string, settings roomChatGrants, redact historyRedactor) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, id := range []string{event.EventID, event.CausationID} {
		if id != "" && (!protocol.ValidMentionEventID(id) || redact.text(id) != id) {
			return false, errors.New("invalid chat event or reply ID")
		}
	}

	c.ws.eventMu.Lock()
	defer c.ws.eventMu.Unlock()
	if event.EventID != "" {
		old, err := c.ws.Store.EventByID(ctx, event.RoomID, event.EventID)
		if err != nil {
			return false, err
		}
		if old != nil {
			payload, _ := old.Payload.(map[string]any)
			if old.Actor != event.Actor || old.Type != event.Type || old.CausationID != event.CausationID || payload["text"] != text {
				return false, errors.New("chat event ID conflicts with a committed event")
			}
			*event = *old
			return false, nil
		}
	}

	var recipients []protocol.Actor
	if event.CausationID == "" {
		recipients = agentPostRecipients(text, event.Actor, settings, time.Now())
	} else {
		original, err := c.ws.Store.EventByID(ctx, event.RoomID, event.CausationID)
		if err != nil {
			return false, err
		}
		if original == nil || original.Type != "chat.message" {
			return false, errors.New("reply is outside this room's chat history")
		}
	}
	if err := c.ws.Store.AppendTrackedRoomChat(ctx, event, recipients, ""); err != nil {
		return false, err
	}
	c.ws.Bus.Publish(*event)
	return true, nil
}

func agentPostRecipients(text string, sender protocol.Actor, settings roomChatGrants, now time.Time) []protocol.Actor {
	if !strings.HasPrefix(text, "@") {
		return nil
	}
	end := strings.IndexFunc(text, unicode.IsSpace)
	if end <= 1 || strings.TrimSpace(text[end:]) == "" {
		return nil
	}
	target := text[1:end]
	if target == "所有人" {
		if !settings.members[sender].Broadcast {
			return nil
		}
		var recipients []protocol.Actor
		for actor, member := range settings.members {
			if actor != sender && now.Before(member.ExpiresAt) {
				recipients = append(recipients, actor)
			}
		}
		return recipients
	}
	actor := protocol.Actor{Kind: "agent", ID: target}
	if member, ok := settings.members[actor]; ok && actor != sender && now.Before(member.ExpiresAt) {
		return []protocol.Actor{actor}
	}
	return nil
}
