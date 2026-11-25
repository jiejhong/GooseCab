package localtransport

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

const requestTimeout = 5 * time.Second

type PullClient struct {
	conn    *websocket.Conn
	room    string
	agent   string
	gateway bool
	mu      sync.Mutex
}

func DialPull(ctx context.Context, raw, room, id, token string) (*PullClient, error) {
	conn, err := dial(ctx, raw, room, id, token, protocol.LocalRoomPullMode, false)
	if err != nil {
		return nil, err
	}
	return &PullClient{conn: conn, room: room, agent: id}, nil
}

func DialGatewayRoom(ctx context.Context, raw, room, id, token string) (*PullClient, error) {
	conn, err := dial(ctx, raw, room, id, token, protocol.LocalRoomPullMode, true)
	if err != nil {
		return nil, err
	}
	return &PullClient{conn: conn, room: room, agent: id, gateway: true}, nil
}

func (c *PullClient) Close() error { return c.conn.Close() }

func (c *PullClient) ReadLocalRoomPage(ctx context.Context, query protocol.LocalRoomReadQuery) (protocol.LocalRoomReadReply, error) {
	var result protocol.LocalRoomReadReply
	kind, err := c.localRoomExchange(ctx, map[string]any{"type": "room.read", "read": query}, &result)
	if err != nil {
		return protocol.LocalRoomReadReply{}, err
	}
	if kind != "room.read.result" || !c.validLocalRoomPage(query, result) {
		_ = c.Close()
		return protocol.LocalRoomReadReply{}, errors.New("invalid room_read result; no unread confirmation sent")
	}
	return result, nil
}

func (c *PullClient) ConfirmLocalRoomPage(ctx context.Context, receipt protocol.LocalRoomPageReceipt) error {
	var reply struct {
		DeliveryID string `json:"delivery_id"`
	}
	kind, err := c.localRoomExchange(ctx, map[string]any{"type": "mention.page_ack", "receipt": receipt}, &reply)
	if err != nil {
		return err
	}
	if kind != "mention.page_acknowledged" || reply.DeliveryID != receipt.DeliveryID {
		_ = c.Close()
		return errors.New("room_read acknowledgement not confirmed")
	}
	return nil
}

func (c *PullClient) localRoomExchange(ctx context.Context, request, response any) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	deadline := time.Now().Add(requestTimeout)
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return "", err
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return "", err
	}
	if err := c.conn.WriteJSON(request); err != nil {
		_ = c.Close()
		return "", err
	}
	messageType, raw, err := c.conn.ReadMessage()
	if err != nil {
		_ = c.Close()
		return "", err
	}
	var header struct {
		Type string `json:"type"`
		Code string `json:"code"`
	}
	if messageType != websocket.TextMessage || json.Unmarshal(raw, &header) != nil {
		_ = c.Close()
		return "", errors.New("invalid isolated Agent reply")
	}
	if header.Type == "error" {
		// Only errors understood by the client are printed. Frames and
		// delivery proofs stay out of diagnostics.
		switch header.Code {
		case "invalid_history_cursor", "invalid_history_range", "room_unavailable", "page_too_large", "unconfirmed_delivery", "ack_failed":
			return "", fmt.Errorf("isolated Agent operation rejected (%s)", header.Code)
		default:
			return "", errors.New("isolated Agent operation rejected")
		}
	}
	if err := json.Unmarshal(raw, response); err != nil {
		_ = c.Close()
		return "", errors.New("invalid isolated Agent reply payload")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return header.Type, nil
}

func (c *PullClient) validLocalRoomPage(query protocol.LocalRoomReadQuery, result protocol.LocalRoomReadReply) bool {
	page := result.Page
	if page == nil || page.RoomID != c.room || page.AgentID != c.agent ||
		page.Events == nil || len(page.Events) > query.Limit {
		return false
	}
	nextSeq := query.AfterSeq
	inPage := make(map[string]bool, len(page.Events))
	for i, event := range page.Events {
		if event.EventID == "" || event.RoomID != c.room || (event.Type == "" || (!c.gateway && event.Type != "chat.message")) ||
			event.Seq < 1 || event.Seq < nextSeq || (i == 0 && query.AfterCursor == "" && event.Seq == nextSeq) ||
			inPage[event.EventID] {
			return false
		}
		nextSeq = event.Seq
		inPage[event.EventID] = true
	}
	if page.NextSeq != nextSeq {
		return false
	}
	if len(page.Events) == 0 {
		return !page.HasMore && page.NextCursor == query.AfterCursor && result.Receipt == nil && len(page.PrivateMentionEventIDs) == 0
	}
	row, err := strconv.Atoi(page.NextCursor)
	if err != nil || row < 1 || (!c.gateway && row < len(page.Events)) {
		return false
	}
	if query.AfterCursor != "" {
		before, err := strconv.Atoi(query.AfterCursor)
		if err != nil || (!c.gateway && row-len(page.Events) != before) ||
			(c.gateway && page.NextSeq == query.AfterSeq && row <= before) {
			return false
		}
	}
	if ids := page.PrivateMentionEventIDs; len(ids) > 0 {
		if !c.gateway || (result.Receipt != nil && !slices.Equal(ids, result.Receipt.EventIDs)) {
			return false
		}
		seen := make(map[string]bool, len(ids))
		for _, id := range ids {
			if !inPage[id] || seen[id] {
				return false
			}
			seen[id] = true
		}
	}
	if result.Receipt == nil {
		return true
	}
	proof, err := hex.DecodeString(result.Receipt.DeliveryID)
	if err != nil || len(proof) != 16 || len(result.Receipt.EventIDs) == 0 {
		return false
	}
	for _, id := range result.Receipt.EventIDs {
		if !inPage[id] {
			return false
		}
		delete(inPage, id)
	}
	return true
}
