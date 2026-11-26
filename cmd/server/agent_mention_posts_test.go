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

func TestAgentChatPostPrivateMentionIngress(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", fixtureLegacyToken)
	path := filepath.Join(t.TempDir(), "agent-mention.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const room = "agent-mention-fixture"
	const target = "fixture-target"
	human, humanToken := roomChatFixtureMember("fixture-human", 1, true, false)
	sender, senderToken := roomChatFixtureMember("fixture-sender", 2, true, false)
	recipient, recipientToken := roomChatFixtureMember(target, 3, true, false)
	broadcaster, broadcasterToken := roomChatFixtureMember("fixture-caster", 4, true, true)
	expired, _ := roomChatFixtureMember("fixture-expired", 5, true, false)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	cfg := &gateway.RoomChatConfig{Rooms: []gateway.RoomChatRoom{{
		ID: room, TrackMentionReceipts: true, AgentMentionPosts: true,
		Humans: []gateway.RoomChatMember{human}, Agents: []gateway.RoomChatMember{sender, recipient, broadcaster, expired},
	}}}
	server := startProtectedRoomFixture(t, db, path, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	wsURL := func(path string) string { return "ws" + strings.TrimPrefix(server.URL, "http") + path }
	dial := func(roomID, actor, credential string) *chatmcp.GatewayBackend {
		t.Helper()
		client, err := chatmcp.Connect(ctx, chatmcp.Config{
			URL: wsURL("/api/v1/agent/connect"), Room: roomID, AgentID: actor, Token: credential,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	send := func(b *chatmcp.GatewayBackend, args chatmcp.SendArgs) {
		t.Helper()
		c, end := context.WithTimeout(ctx, 3*time.Second)
		defer end()
		reply, err := b.Send(c, args)
		wire, _ := json.Marshal(reply)
		if err != nil || !bytes.Contains(wire, []byte(args.MessageID)) || !bytes.Contains(wire, []byte("persisted")) {
			t.Fatalf("ordinary chat_send did not receive its committed echo: %s (%v)", wire, err)
		}
	}
	statusPath := func(id string) string { return "/api/room-chat/mentions/status?room_id=" + room + "&event_id=" + id }
	status := func(id, token string) []store.MentionStatus {
		t.Helper()
		body := roomChatHTTP(t, server, http.MethodGet, statusPath(id), "Bearer "+token, "", "", 200)
		var result struct{ Recipients []store.MentionStatus }
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		return result.Recipients
	}
	check := func(id, state string) {
		t.Helper()
		rows := status(id, senderToken)
		if len(rows) != 1 || rows[0].Recipient.ID != target || rows[0].State != state {
			t.Fatalf("%s has incorrect private receipt: %+v", id, rows)
		}
	}

	// The default shared ingress still accepts chat but cannot create a private
	// notice, even if its locally asserted Agent ID matches a trusted sender.
	legacy := dial("demo", sender.ID, fixtureLegacyToken)
	send(legacy, chatmcp.SendArgs{MessageID: "legacy:mention", Text: "@" + target + " ordinary demo text"})
	_ = legacy.Close()
	if pending, err := db.PendingMentions(ctx, "demo", protocol.Actor{Kind: "agent", ID: target}, 16); err != nil || len(pending) != 0 {
		t.Fatalf("legacy room generated a private notice: %v", err)
	}
	if forged, err := chatmcp.Connect(ctx, chatmcp.Config{URL: wsURL("/api/v1/agent/connect"),
		Room: room, AgentID: sender.ID, Token: fixtureLegacyToken}); err == nil {
		_ = forged.Close()
		t.Fatal("shared token opened the protected sender path")
	}
	native := dial(room, sender.ID, senderToken)
	direct := chatmcp.SendArgs{MessageID: "fixture-sender:direct-1", Text: "@" + target + " synthetic request"}
	if rows := status("legacy:mention", senderToken); len(rows) != 0 {
		t.Fatal("legacy room generated an isolated notice")
	}
	for i, draft := range []chatmcp.SendArgs{
		{MessageID: "plain-1", Text: "literal @" + target + " in ordinary text"},
		{MessageID: "plain-2", Text: "@unknown-agent no recipient"},
		{MessageID: "plain-3", Text: "@所有人 unauthorized broadcast"},
		{MessageID: "plain-4", Text: "@" + target},
	} {
		send(native, draft)
		if rows := status(draft.MessageID, senderToken); len(rows) != 0 {
			t.Fatalf("plain case %d generated a notification", i)
		}
	}
	send(native, direct)
	check(direct.MessageID, "pending")
	view, err := native.MentionStatus(ctx, direct.MessageID)
	viewJSON, _ := json.Marshal(view)
	if err != nil || !bytes.Contains(viewJSON, []byte(`"state":"pending"`)) {
		t.Fatalf("ordinary Agent client could not read its receipt: %s %v", viewJSON, err)
	}
	send(native, chatmcp.SendArgs{MessageID: "plain-reply", Text: "@" + target + " reply text", ReplyTo: direct.MessageID})
	if rows := status("plain-reply", senderToken); len(rows) != 0 {
		t.Fatal("reply generated another notice")
	}
	if rows := status(direct.MessageID, broadcasterToken); len(rows) != 0 {
		t.Fatal("unrelated Agent saw another author's receipts")
	}
	roomChatHTTP(t, server, http.MethodGet, statusPath(direct.MessageID), "Bearer "+fixtureLegacyToken, "", "", http.StatusUnauthorized)
	_ = native.Close()
	server.Close()

	// Reopen the Gateway with the same database while the recipient still has
	// no connection. Retrying the same send must produce the same event.
	server = startProtectedRoomFixture(t, db, path, cfg)
	native = dial(room, sender.ID, senderToken)
	send(native, direct)
	check(direct.MessageID, "pending")
	getPull := func() *localtransport.PullClient {
		t.Helper()
		client, err := localtransport.DialGatewayRoom(ctx, wsURL("/api/room-chat/agent"), room, target, recipientToken)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	pull := getPull()
	page, err := pull.ReadLocalRoomPage(ctx, protocol.LocalRoomReadQuery{Limit: 20})
	if err != nil || page.Receipt != nil {
		t.Fatalf("ordinary history changed mention status: %v", err)
	}
	check(direct.MessageID, "pending")
	notice, err := pull.NextMention(ctx)
	if err != nil || notice.Page == nil || len(notice.Page.Events) != 1 ||
		notice.Page.Events[0].EventID != direct.MessageID || notice.Receipt == nil {
		t.Fatalf("addressed transport did not receive the stable persisted post: %v", err)
	}
	check(direct.MessageID, "pending")
	if err := pull.ConfirmLocalRoomPage(ctx, *notice.Receipt); err != nil {
		t.Fatal(err)
	}
	check(direct.MessageID, "received")
	const replyID = "0671ac38-42b8-480f-b2a9-d67138c253c8"
	if reply, err := pull.SendMentionReply(ctx, protocol.MentionReply{EventID: direct.MessageID, MessageID: replyID, Text: "synthetic explicit reply"}); err != nil ||
		reply.Actor != (protocol.Actor{Kind: "agent", ID: target}) {
		t.Fatalf("authenticated reply failed: %v", err)
	}
	check(direct.MessageID, "replied")
	if _, err := pull.SendMentionReply(ctx, protocol.MentionReply{EventID: direct.MessageID, MessageID: replyID, Text: "synthetic explicit reply"}); err == nil {
		t.Fatal("duplicate reply became a new event")
	}
	_ = pull.Close()
	pull = getPull()
	if empty, err := pull.NextMention(ctx); err != nil || empty.Page == nil || len(empty.Page.Events) != 0 {
		t.Fatal("confirmed notice returned again after reconnect", err)
	}
	events, err := db.EventsAfter(ctx, room, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.EventID == direct.MessageID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("retry persisted %d copies of the original", count)
	}
	caster := dial(room, broadcaster.ID, broadcasterToken)
	send(caster, chatmcp.SendArgs{MessageID: "caster:all", Text: "@所有人 synthetic broadcast"})
	rows := status("caster:all", broadcasterToken)
	if len(rows) != 3 || rows[0].Recipient.ID != sender.ID || rows[1].Recipient.ID != target || rows[2].Recipient.ID != human.ID {
		t.Fatalf("wrong broadcast snapshot: %+v", rows)
	}
	inbox := roomChatHTTP(t, server, http.MethodGet, "/api/room-chat/mentions?room_id="+room, "GooseCabRoom "+humanToken, "", "", 200)
	if bytes.Contains(inbox, []byte(direct.MessageID)) || !bytes.Contains(inbox, []byte("caster:all")) {
		t.Fatal("human recipient did not get only their own notice")
	}
}
