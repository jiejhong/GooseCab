package localtransport

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func (c *PullClient) NextMention(ctx context.Context) (protocol.LocalRoomReadReply, error) {
	var result protocol.LocalRoomReadReply
	kind, err := c.localRoomExchange(ctx, map[string]string{"type": "mention.next"}, &result)
	if err != nil {
		return result, err
	}
	if kind != "mention.next.result" || !c.validMention(result) {
		_ = c.Close()
		return protocol.LocalRoomReadReply{}, errors.New("invalid mention notification; no receipt confirmed")
	}
	return result, nil
}

func (c *PullClient) validMention(result protocol.LocalRoomReadReply) bool {
	page := result.Page
	if page == nil || page.RoomID != c.room || page.AgentID != c.agent || page.Events == nil || len(page.Events) > 1 {
		return false
	}
	if len(page.Events) == 0 {
		return result.Receipt == nil
	}
	event := page.Events[0]
	if !protocol.ValidMentionEventID(event.EventID) || event.RoomID != c.room || event.Type != "chat.message" ||
		event.Seq < 1 || result.Receipt == nil || len(result.Receipt.EventIDs) != 1 ||
		result.Receipt.EventIDs[0] != event.EventID {
		return false
	}
	proof, err := hex.DecodeString(result.Receipt.DeliveryID)
	return err == nil && len(proof) == 16
}

func (c *PullClient) SendMentionReply(ctx context.Context, reply protocol.MentionReply) (protocol.Event, error) {
	var response struct {
		Event protocol.Event `json:"event"`
	}
	kind, err := c.localRoomExchange(ctx, map[string]any{"type": "mention.reply", "reply": reply}, &response)
	if err != nil {
		return protocol.Event{}, err
	}
	event := response.Event
	if kind != "mention.reply.result" || event.EventID != reply.MessageID || event.RoomID != c.room ||
		event.Actor != (protocol.Actor{Kind: "agent", ID: c.agent}) || event.Type != "chat.message" || event.Seq < 1 {
		_ = c.Close()
		return protocol.Event{}, errors.New("mention reply commit not confirmed")
	}
	return event, nil
}
