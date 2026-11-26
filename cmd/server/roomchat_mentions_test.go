package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/agent/localtransport"
	"github.com/jiejhong/GooseCab/internal/chatmcp"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/store"
)

func TestPrivateMentionThroughBrowserAPIAndAgentMCP(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", fixtureLegacyToken)
	path := filepath.Join(t.TempDir(), "mentions.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sender, senderToken := roomChatFixtureMember("sender", 1, true, true)
	other, otherToken := roomChatFixtureMember("other", 2, true, false)
	reader, readerToken := roomChatFixtureMember("reader", 3, false, false)
	agent, agentToken := roomChatFixtureMember("worker", 4, true, false)
	expired, _ := roomChatFixtureMember("expired", 5, false, false)
	expired.ExpiresAt = time.Now().Add(-time.Second)
	outsider, outsiderToken := roomChatFixtureMember("outsider", 6, true, true)
	const room = "mentions-fixture"
	cfg := &gateway.RoomChatConfig{Rooms: []gateway.RoomChatRoom{
		{ID: room, TrackMentionReceipts: true, Humans: []gateway.RoomChatMember{sender, other, reader, expired}, Agents: []gateway.RoomChatMember{agent}},
		{ID: "different-room", TrackMentionReceipts: true, Humans: []gateway.RoomChatMember{outsider}},
	}}
	server := startProtectedRoomFixture(t, db, path, cfg)
	auth := func(token string) string { return "GooseCabRoom " + token }
	get := func(path, token string, status int) []byte {
		t.Helper()
		return roomChatHTTP(t, server, http.MethodGet, path, token, "", "", status)
	}
	post := func(path, token, body string, status int) protocol.Event {
		t.Helper()
		raw := roomChatHTTP(t, server, http.MethodPost, path, auth(token), cfg.Origin, body, status)
		var event protocol.Event
		if status == http.StatusCreated && json.Unmarshal(raw, &event) != nil {
			t.Fatal("invalid event receipt")
		}
		return event
	}
	const suffix = "?room_id=" + room
	const messages = "/api/room-chat/messages" + suffix
	const inbox = "/api/room-chat/mentions" + suffix
	const ack = "/api/room-chat/mentions/ack" + suffix
	post(messages, otherToken, `{"text":"unauthorized broadcast","mention_all":true}`, http.StatusForbidden)
	get(inbox, "", http.StatusUnauthorized)
	get(inbox, "Bearer "+fixtureLegacyToken, http.StatusUnauthorized)
	get(inbox, auth(outsiderToken), http.StatusUnauthorized)
	get(inbox, auth(agentToken), http.StatusUnauthorized)

	statuses := func(id, token string) []store.MentionStatus {
		t.Helper()
		raw := get("/api/room-chat/mentions/status"+suffix+"&event_id="+id, auth(token), http.StatusOK)
		var reply struct{ Recipients []store.MentionStatus }
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatal(err)
		}
		return reply.Recipients
	}
	plain := post(messages, otherToken, `{"text":"plain @worker"}`, http.StatusCreated)
	if rows := statuses(plain.EventID, otherToken); len(rows) != 0 {
		t.Fatal("plain text emitted a notification")
	}
	original := post(messages, senderToken, `{"text":"direct private notice","mention":{"kind":"agent","id":"worker"}}`, http.StatusCreated)
	check := func(want string) store.MentionStatus {
		t.Helper()
		rows := statuses(original.EventID, senderToken)
		if len(rows) != 1 || rows[0].Recipient != (protocol.Actor{Kind: "agent", ID: "worker"}) || rows[0].State != want {
			t.Fatalf("unexpected direct mention status: %+v", rows)
		}
		return rows[0]
	}
	check("pending")
	if len(statuses(original.EventID, otherToken)) != 0 || !bytes.Contains(get(inbox, auth(otherToken), 200), []byte(`"items":[]`)) {
		t.Fatal("unaddressed member saw a notification or status")
	}
	post(ack, otherToken, `{"event_id":"`+original.EventID+`","proof":"plain-history"}`, http.StatusForbidden)
	get("/api/room-chat/history"+suffix, auth(readerToken), 200)
	check("pending")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ws := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/room-chat/agent"
	call := func(reader *chatmcp.LocalRoomReader, tool string, args any) (string, bool) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
		request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":` + string(params) + "}\n"
		var out bytes.Buffer
		if err := (&chatmcp.Server{LocalRoom: reader, Mentions: true}).Serve(ctx, strings.NewReader(request), &out); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Result struct {
				Content []struct{ Text string }
				IsError bool
			}
		}
		lines := strings.Split(out.String(), "\n")
		if len(lines) != 3 || json.Unmarshal([]byte(lines[1]), &response) != nil || len(response.Result.Content) != 1 {
			t.Fatal("unexpected MCP response")
		}
		return response.Result.Content[0].Text, response.Result.IsError
	}
	if reader, err := chatmcp.ConnectGatewayRoom(ctx, ws, room, "worker", fixtureLegacyToken); err == nil {
		reader.Close()
		t.Fatal("shared token connected to private notices")
	}
	dial := func() *chatmcp.LocalRoomReader {
		t.Helper()
		reader, err := chatmcp.ConnectGatewayRoom(ctx, ws, room, "worker", agentToken)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reader.Close() })
		return reader
	}
	connection := dial()
	if value, failed := call(connection, "room_read", map[string]int{"limit": 20}); failed ||
		!strings.Contains(value, original.EventID) || strings.Contains(value, "mention_receipt") || strings.Contains(value, `"mention":`) {
		t.Fatal("ordinary history lost the message or exposed private delivery fields")
	}
	check("pending")
	if value, failed := call(connection, "mention_next", struct{}{}); failed || !strings.Contains(value, original.EventID) {
		t.Fatal("addressed MCP did not deliver the notification")
	}
	check("received")
	_ = connection.Close()
	reconnected := dial()
	if value, failed := call(reconnected, "mention_next", struct{}{}); failed || !strings.Contains(value, `"events":[]`) {
		t.Fatal("confirmed notice fired again after reconnect")
	}
	const replyID = "0671ac38-42b8-480f-b2a9-d67138c253c8"
	if value, failed := call(reconnected, "mention_reply", protocol.MentionReply{EventID: original.EventID, MessageID: replyID, Text: "reply fixture"}); failed ||
		!strings.Contains(value, replyID) {
		t.Fatal("agent reply was not committed")
	}
	if row := check("replied"); row.ReplyEventID != replyID {
		t.Fatal("reply evidence did not link to the original")
	}
	if _, failed := call(reconnected, "mention_reply", protocol.MentionReply{EventID: original.EventID, MessageID: replyID, Text: "reply fixture"}); !failed {
		t.Fatal("reply was retried as a new send")
	}
	post(messages, otherToken, `{"text":"not the addressed member","reply_to_event_id":"`+original.EventID+`"}`, http.StatusForbidden)

	waiter, err := localtransport.DialGatewayRoom(ctx, ws, room, "worker", agentToken)
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Close()
	type delivery struct {
		reply protocol.LocalRoomReadReply
		err   error
	}
	pending := make(chan delivery, 1)
	go func() {
		reply, err := waiter.NextMention(ctx)
		pending <- delivery{reply, err}
	}()
	time.Sleep(50 * time.Millisecond)
	broadcast := post(messages, senderToken, `{"text":"private room broadcast","mention_all":true}`, http.StatusCreated)
	select {
	case delivered := <-pending:
		if delivered.err != nil || delivered.reply.Page == nil || len(delivered.reply.Page.Events) != 1 ||
			delivered.reply.Page.Events[0].EventID != broadcast.EventID {
			t.Fatalf("waiting agent did not get the committed notice: %+v", delivered)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new mention did not wake the waiting agent")
	}
	rows := statuses(broadcast.EventID, senderToken)
	if len(rows) != 3 || rows[0].Recipient.ID != "worker" || rows[1].Recipient.ID != "other" || rows[2].Recipient.ID != "reader" {
		t.Fatalf("unexpected broadcast snapshot: %+v", rows)
	}
	var notifications struct {
		Items []struct {
			Event protocol.Event
			Proof string
		}
	}
	if err := json.Unmarshal(get(inbox, auth(readerToken), 200), &notifications); err != nil || len(notifications.Items) != 1 ||
		notifications.Items[0].Event.EventID != broadcast.EventID {
		t.Fatal("read-only member did not obtain their own broadcast notice")
	}
	body, _ := json.Marshal(map[string]string{"event_id": broadcast.EventID, "proof": notifications.Items[0].Proof})
	post(ack, otherToken, string(body), http.StatusForbidden)
	post(ack, readerToken, string(body), http.StatusNoContent)
	if rows = statuses(broadcast.EventID, senderToken); len(rows) != 3 || rows[0].State != "pending" || rows[1].State != "pending" || rows[2].State != "received" {
		t.Fatalf("browser receipt did not update just its own row: %+v", rows)
	}
}
