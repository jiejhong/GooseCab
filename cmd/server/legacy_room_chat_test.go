package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jiejhong/GooseCab/internal/agent"
	"github.com/jiejhong/GooseCab/internal/agent/localtransport"
	"github.com/jiejhong/GooseCab/internal/gateway"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/relay"
	"github.com/jiejhong/GooseCab/internal/store"
)

func TestLegacyRoomChatKeepsMentionsOffLegacyRoutes(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", fixtureLegacyToken)
	path := filepath.Join(t.TempDir(), "existing-room.db")
	createPriorRoomChatDB(t, path)
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer, writerToken := roomChatFixtureMember("writer", 1, true, false)
	secondWriter, secondToken := roomChatFixtureMember("second-writer", 2, true, false)
	reader, readerToken := roomChatFixtureMember("private-human-fixture", 3, false, false)
	worker, workerToken := roomChatFixtureMember("private-agent-fixture", 4, false, false)
	other, otherToken := roomChatFixtureMember("other-agent", 5, false, false)
	cfg := &gateway.RoomChatConfig{RequireUnusedRooms: true, Rooms: []gateway.RoomChatRoom{
		{ID: "demo", LegacyReadersMayReadNewMessages: true, Humans: []gateway.RoomChatMember{writer, secondWriter, reader},
			Agents: []gateway.RoomChatMember{worker, other}},
	}}
	server := startProtectedRoomFixture(t, db, path, cfg)
	defer server.Close()
	if err := db.CheckRoomChatProtection(context.Background(), nil); err != nil {
		t.Fatal("legacy demo unexpectedly acquired a mandatory read-protection marker", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(server.URL, "http")
	legacy, err := agent.DialWithTokenContext(ctx, base+"/api/v1/agent/connect", "demo", worker.ID, nil, fixtureLegacyToken)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	legacyAuth := "Bearer " + fixtureLegacyToken
	observer, _, err := websocket.DefaultDialer.DialContext(ctx, base+"/ws?room_id=demo", http.Header{"Authorization": {legacyAuth}})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	deadline, _ := ctx.Deadline()
	_ = observer.SetReadDeadline(deadline)

	human := func(token string) string { return "GooseCabRoom " + token }
	route := "/api/room-chat/messages?room_id=demo"
	roomChatHTTP(t, server, "POST", route, legacyAuth, cfg.Origin, `{"text":"denied"}`, 401)
	roomChatHTTP(t, server, "POST", route, human(readerToken), cfg.Origin, `{"text":"denied"}`, 403)
	roomChatHTTP(t, server, "POST", route, human(writerToken), cfg.Origin, `{"text":"denied","mention_all":true}`, 403)
	agentPost := roomChatHTTP(t, server, "POST", route, human(writerToken), cfg.Origin,
		`{"text":"public sample for agent","mention":{"kind":"agent","id":"private-agent-fixture"}}`, 201)
	var agentEvent protocol.Event
	if json.Unmarshal(agentPost, &agentEvent) != nil || agentEvent.EventID == "" || agentEvent.Actor != (protocol.Actor{Kind: "human", ID: writer.ID}) {
		t.Fatal("unexpected public message receipt")
	}
	humanPost := roomChatHTTP(t, server, "POST", route, human(secondToken), cfg.Origin,
		`{"text":"public sample for human","mention":{"kind":"human","id":"private-human-fixture"}}`, 201)
	var humanEvent protocol.Event
	if json.Unmarshal(humanPost, &humanEvent) != nil || humanEvent.Seq != agentEvent.Seq+1 {
		t.Fatal("personal mentions added extra public event IDs or sequences")
	}

	for _, stream := range []func() (map[string]any, error){
		func() (map[string]any, error) { return legacy.ReadContext(ctx) },
		func() (map[string]any, error) {
			var event map[string]any
			err := observer.ReadJSON(&event)
			return event, err
		},
	} {
		last := ""
		seenHistory := false
		for last != humanEvent.EventID {
			event, err := stream()
			if err != nil {
				t.Fatal(err)
			}
			last, _ = event["event_id"].(string)
			seenHistory = seenHistory || last == "before-a"
			if last == agentEvent.EventID || last == humanEvent.EventID {
				payload, ok := event["payload"].(map[string]any)
				if !ok || payload["text"] == nil || payload["mention"] != nil || payload["mention_all"] != nil {
					t.Fatal("legacy live event did not have a plain public payload")
				}
			}
		}
		if !seenHistory {
			t.Fatal("legacy stream omitted existing history")
		}
	}
	if err := legacy.SendChat("old shared Agent still sends"); err != nil {
		t.Fatal(err)
	}
	for {
		var frame protocol.Event
		if err := observer.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if body, ok := frame.Payload.(map[string]any); ok && body["text"] == "old shared Agent still sends" {
			break
		}
	}

	replayURL := fmt.Sprintf("%s/ws?room_id=demo&resume_seq=%d", base, agentEvent.Seq-1)
	replay, _, err := websocket.DefaultDialer.DialContext(ctx, replayURL, http.Header{"Authorization": {legacyAuth}})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	_ = replay.SetReadDeadline(deadline)
	for _, expected := range []protocol.Event{agentEvent, humanEvent} {
		var frame protocol.Event
		err := replay.ReadJSON(&frame)
		body, ok := frame.Payload.(map[string]any)
		if err != nil || frame.EventID != expected.EventID || !ok || body["text"] == nil || body["mention"] != nil || body["mention_all"] != nil {
			t.Fatal("legacy reconnect replay did not preserve the public message view", frame.EventID, err)
		}
	}
	_ = replay.Close()

	for _, path := range []string{"/api/events?room_id=demo", "/api/events/export.html?room_id=demo"} {
		body := roomChatHTTP(t, server, "GET", path, legacyAuth, "", "", 200)
		if !bytes.Contains(body, []byte("prior conversation")) || !bytes.Contains(body, []byte("public sample for human")) ||
			bytes.Contains(body, []byte(reader.ID)) || bytes.Contains(body, []byte(`"mention"`)) {
			t.Fatal("legacy history or export changed audience or disclosed personal mention metadata", path)
		}
	}
	roomChatHTTP(t, server, "GET", "/api/events?room_id=demo", "", "", "", 401)
	roomChatHTTP(t, server, "GET", "/api/room-chat/export.html?room_id=demo", human(readerToken), "", "", 200)
	for _, row := range []struct {
		token string
		want  int
	}{{readerToken, 1}, {writerToken, 0}, {secondToken, 0}} {
		out := roomChatHTTP(t, server, "GET", "/api/room-chat/unread?room_id=demo", human(row.token), "", "", 200)
		var unread struct {
			Count  int              `json:"unread_count"`
			Events []protocol.Event `json:"events"`
		}
		if json.Unmarshal(out, &unread) != nil || unread.Count != row.want ||
			(row.want == 1 && (len(unread.Events) != 1 || unread.Events[0].EventID != humanEvent.EventID)) {
			t.Fatal("personal browser notice reached the wrong identity")
		}
	}
	ack := `{"event_ids":["` + humanEvent.EventID + `"]}`
	roomChatHTTP(t, server, "POST", "/api/room-chat/unread/ack?room_id=demo", legacyAuth, cfg.Origin, ack, 401)
	roomChatHTTP(t, server, "POST", "/api/room-chat/unread/ack?room_id=demo", human(writerToken), cfg.Origin, ack, 204)
	if _, count, err := db.RoomChatUnread(ctx, "demo", protocol.Actor{Kind: "human", ID: reader.ID}, 20); err != nil || count != 1 {
		t.Fatal("another browser identity affected a private acknowledgement", count, err)
	}
	roomChatHTTP(t, server, "POST", "/api/room-chat/unread/ack?room_id=demo", human(readerToken), cfg.Origin, ack, 204)
	if _, count, err := db.RoomChatUnread(ctx, "demo", protocol.Actor{Kind: "human", ID: reader.ID}, 20); err != nil || count != 0 {
		t.Fatal("recipient did not confirm its own mention", count, err)
	}

	endpoint := base + "/api/room-chat/agent"
	if conn, err := localtransport.DialGatewayRoom(ctx, endpoint, "demo", worker.ID, fixtureLegacyToken); err == nil {
		_ = conn.Close()
		t.Fatal("shared Agent credential reached the personal delivery route")
	}
	pull := func(id, token string) *localtransport.PullClient {
		t.Helper()
		client, err := localtransport.DialGatewayRoom(ctx, endpoint, "demo", id, token)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	query := protocol.LocalRoomReadQuery{AfterSeq: agentEvent.Seq - 1, Limit: 2}
	otherPull := pull(other.ID, otherToken)
	defer otherPull.Close()
	reply, err := otherPull.ReadLocalRoomPage(ctx, query)
	if err != nil || reply.Receipt != nil || len(reply.Page.PrivateMentionEventIDs) != 0 {
		t.Fatal("another authorized Agent received personal delivery metadata", err)
	}
	recipient := pull(worker.ID, workerToken)
	defer recipient.Close()
	ownPage, err := recipient.ReadLocalRoomPage(ctx, query)
	if err != nil || ownPage.Receipt == nil || !reflect.DeepEqual(ownPage.Page.PrivateMentionEventIDs, []string{agentEvent.EventID}) ||
		!reflect.DeepEqual(ownPage.Receipt.EventIDs, []string{agentEvent.EventID}) {
		t.Fatal("recipient did not receive only its own acknowledgement proof", err)
	}
	if err := recipient.ConfirmLocalRoomPage(ctx, *ownPage.Receipt); err != nil {
		t.Fatal(err)
	}
	after, err := recipient.ReadLocalRoomPage(ctx, query)
	if err != nil || after.Receipt != nil || len(after.Page.PrivateMentionEventIDs) != 0 {
		t.Fatal("receipt did not clear only the delivered Agent notice", err)
	}
}

func TestLegacyAudienceCannotReleaseProtectedRoom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guarded-room.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ProtectRoomChat(context.Background(), []string{"demo"}, false); err != nil {
		t.Fatal(err)
	}
	writer, _ := roomChatFixtureMember("writer", 1, true, false)
	config := &gateway.RoomChatConfig{Origin: "https://gateway.example.invalid", Rooms: []gateway.RoomChatRoom{
		{ID: "demo", LegacyReadersMayReadNewMessages: true, Humans: []gateway.RoomChatMember{writer}},
	}}
	if _, err := assembleServerHandler(db, ":0", path, relay.Config{}, config); err == nil {
		t.Fatal("legacy audience configuration reopened a previously protected room")
	}
}
