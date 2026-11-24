package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func TestRoomChatAtomicAppendAndPrivateAcknowledgement(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnableRoomChat(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_receipt BEFORE INSERT ON room_chat_unread
		WHEN NEW.recipient='reject' BEGIN SELECT RAISE(ABORT,'synthetic receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	user := protocol.Actor{Kind: "human", ID: "same"}
	agent := protocol.Actor{Kind: "agent", ID: "same"}
	event := protocol.Event{RoomID: "demo", Type: "chat.message", Actor: user, Payload: map[string]string{"text": "fixture"}}
	if err := s.AppendRoomChat(ctx, &event, []protocol.Actor{agent, {Kind: "human", ID: "reject"}}); err == nil || event.EventID != "" {
		t.Fatal("failed receipt did not abort append")
	}
	page, err := s.History(ctx, "demo", 0, "", nil, nil, 10)
	if err != nil || len(page.Events) != 0 {
		t.Fatal("failed receipt committed the event")
	}
	_, count, err := s.RoomChatUnread(ctx, "demo", agent, 20)
	if err != nil || count != 0 {
		t.Fatal("failed event committed an earlier receipt")
	}
	if err := s.AppendRoomChat(ctx, &event, []protocol.Actor{user, agent}); err != nil {
		t.Fatal(err)
	}
	for _, room := range []string{"elsewhere", "demo", "demo"} {
		if err := s.AcknowledgeRoomChat(ctx, room, agent, []string{event.EventID}); err != nil {
			t.Fatal(err)
		}
	}
	_, count, err = s.RoomChatUnread(ctx, "demo", user, 20)
	_, agentCount, agentErr := s.RoomChatUnread(ctx, "demo", agent, 20)
	if err != nil || agentErr != nil || count != 1 || agentCount != 0 {
		t.Fatal("room or actor receipt isolation failed")
	}
}
