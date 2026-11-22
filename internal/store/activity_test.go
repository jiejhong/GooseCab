package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func TestLatestRoomChatActivity(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "activity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	appendEvent := func(id, room, kind string, seq int64, at time.Time, payload any) {
		t.Helper()
		if err := s.Append(ctx, protocol.Event{EventID: id, RoomID: room, Type: kind, Seq: seq, TS: at, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(room string, want *time.Time) {
		t.Helper()
		got, err := s.LatestRoomChatActivity(ctx, room)
		if err != nil || (want == nil && got != nil) || (want != nil && (got == nil || !got.Equal(*want) || got.Location() != time.UTC)) {
			t.Fatalf("room %q: activity %v, want %v, err %v", room, got, want, err)
		}
	}
	check("empty", nil)
	appendEvent("only-hello", "empty", "agent.hello", 1, start, "not a chat")
	appendEvent("only-blank", "empty", "chat.message", 2, start, map[string]any{"text": "　\n", "content": 42})
	check("empty", nil)

	appendEvent("old", "demo", "chat.message", 1, start, map[string]string{"text": "old chat"})
	later := start.Add(2 * time.Second)
	laterWithOffset := later.In(time.FixedZone("legacy", 8*60*60))
	if _, err := s.db.Exec(`INSERT INTO events(event_id,room_id,seq,type,payload,created_at)
		VALUES('legacy','demo',2,'chat','"legacy chat"',?)`, laterWithOffset.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	check("demo", &later)
	latest := start.Add(3 * time.Second)
	appendEvent("duplicate-seq", "demo", "chat.message", 2, latest, map[string]string{"message": "new chat"})
	appendEvent("newer-hello", "demo", "agent.hello", 3, start.Add(4*time.Second), "not a chat")
	appendEvent("newer-result", "demo", "capability.result", 4, start.Add(5*time.Second), map[string]string{"content": "metadata"})
	appendEvent("blank", "demo", "chat.message", 5, start.Add(6*time.Second), map[string]string{"text": " \t\n"})
	appendEvent("future", "demo", "chat.message", 6, time.Now().Add(time.Hour), "future timestamp")
	appendEvent("zero", "demo", "chat.message", 7, time.Time{}, "unknown timestamp")
	if _, err := s.db.Exec(`INSERT INTO events(event_id,room_id,seq,type,payload,created_at) VALUES
		('bad-date','demo',8,'chat.message','"invalid date"','invalid'),
		('bad-json','demo',9,'chat.message','{',?)`, start.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	newerOtherRoom := start.Add(time.Hour)
	appendEvent("other-chat", "other", "chat.message", 50, newerOtherRoom, map[string]string{"content": "elsewhere"})
	check("demo", &latest)
	check("other", &newerOtherRoom)
}
