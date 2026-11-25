package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func TestRecentRoomReadsExpireAndNeverEnumerateGrants(t *testing.T) {
	now := time.Now()
	reader := protocol.Actor{Kind: "agent", ID: "reader"}
	expired := protocol.Actor{Kind: "human", ID: "expired"}
	untouched := protocol.Actor{Kind: "human", ID: "untouched"}
	settings := roomChatGrants{members: map[protocol.Actor]RoomChatMember{
		reader: {ExpiresAt: now.Add(time.Hour)}, expired: {ExpiresAt: now}, untouched: {ExpiresAt: now.Add(time.Hour)},
	}}
	activity := roomChatActivity{last: map[roomChatWriter]time.Time{
		{"here", reader}: now.Add(-recentRoomReadWindow), {"here", expired}: now.Add(-time.Second),
		{"elsewhere", reader}: now,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	activity.record(ctx, "here", untouched)
	if reads := activity.recent("here", settings, now, newHistoryRedactor()); len(reads) != 0 {
		t.Fatalf("old read, expired grant, canceled operation, or another room appeared: %+v", reads)
	}
}
