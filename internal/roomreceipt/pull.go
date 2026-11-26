// Package roomreceipt handles the common same-session pull acknowledgement.
package roomreceipt

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

type command struct {
	Type    string                         `json:"type"`
	Read    *protocol.LocalRoomReadQuery   `json:"read,omitempty"`
	Receipt *protocol.LocalRoomPageReceipt `json:"receipt,omitempty"`
	Reply   *protocol.MentionReply         `json:"reply,omitempty"`
}

type MentionCommands struct {
	Next  func() (protocol.LocalRoomReadReply, string)
	Ack   func(string) error
	Reply func(protocol.MentionReply) (protocol.Event, string)
}

// Serve accepts acknowledgement only for the precise event IDs in the most
// recently delivered page of this authenticated connection.
func Serve(ctx context.Context, conn *websocket.Conn, send func(any) bool, reject func(string) bool,
	page func(protocol.LocalRoomReadQuery) (protocol.LocalRoomReadReply, string), ack func([]string) error, mentions *MentionCommands) {
	var delivered *protocol.LocalRoomPageReceipt
	var deliveredMention bool
	for ctx.Err() == nil {
		messageType, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var cmd command
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if messageType != websocket.TextMessage || !utf8.Valid(raw) || decoder.Decode(&cmd) != nil || decoder.Decode(new(any)) != io.EOF {
			if !reject("invalid_request") {
				return
			}
			continue
		}
		switch {
		case cmd.Type == "room.read" && cmd.Read != nil && cmd.Receipt == nil && cmd.Reply == nil,
			cmd.Type == "mention.next" && cmd.Read == nil && cmd.Receipt == nil && cmd.Reply == nil && mentions != nil:
			delivered = nil
			deliveredMention = cmd.Type == "mention.next"
			var reply protocol.LocalRoomReadReply
			var code string
			if deliveredMention {
				reply, code = mentions.Next()
			} else {
				reply, code = page(*cmd.Read)
			}
			if code != "" {
				if !reject(code) {
					return
				}
				continue
			}
			if reply.Receipt != nil && reply.Receipt.DeliveryID == "" {
				var proof [16]byte
				if _, err := rand.Read(proof[:]); err != nil {
					return
				}
				reply.Receipt.DeliveryID = hex.EncodeToString(proof[:])
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
		case cmd.Type == "mention.page_ack" && cmd.Receipt != nil && cmd.Read == nil && cmd.Reply == nil:
			if delivered == nil || (deliveredMention && len(delivered.EventIDs) != 1) ||
				subtle.ConstantTimeCompare([]byte(delivered.DeliveryID), []byte(cmd.Receipt.DeliveryID)) != 1 ||
				!slices.Equal(delivered.EventIDs, cmd.Receipt.EventIDs) {
				if !reject("unconfirmed_delivery") {
					return
				}
				continue
			}
			err := ctx.Err()
			if err == nil {
				if deliveredMention {
					err = mentions.Ack(delivered.EventIDs[0])
				} else {
					err = ack(delivered.EventIDs)
				}
			}
			if err != nil {
				if !reject("ack_failed") {
					return
				}
				continue
			}
			if !send(map[string]string{"type": "mention.page_acknowledged", "delivery_id": delivered.DeliveryID}) {
				return
			}
		case cmd.Type == "mention.reply" && cmd.Reply != nil && cmd.Read == nil && cmd.Receipt == nil && mentions != nil && mentions.Reply != nil:
			event, code := mentions.Reply(*cmd.Reply)
			if code != "" {
				if !reject(code) {
					return
				}
				continue
			}
			if !send(map[string]any{"type": "mention.reply.result", "event": event}) {
				return
			}
		default:
			if !reject("unsupported_message") {
				return
			}
		}
	}
}
