package gateway

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

const recentRoomReadWindow = 5 * time.Minute

// Room-read activity is process-local and bounded by the configured room grants.
// Long-lived Agent connections use the independent WebSocket tracker.
type roomChatActivity struct {
	mu   sync.Mutex
	last map[roomChatWriter]time.Time
}

func (a *roomChatActivity) record(ctx context.Context, room string, actor protocol.Actor) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if a.last == nil {
		a.last = make(map[roomChatWriter]time.Time)
	}
	a.last[roomChatWriter{room, actor}] = time.Now().UTC()
}

type recentRoomReader struct {
	Actor protocol.Actor `json:"actor"`
	At    time.Time      `json:"at"`
}

func (a *roomChatActivity) recent(room string, settings roomChatGrants, now time.Time, redact historyRedactor) []recentRoomReader {
	a.mu.Lock()
	defer a.mu.Unlock()
	readers := make([]recentRoomReader, 0)
	for actor, grant := range settings.members {
		at := a.last[roomChatWriter{room, actor}]
		if at.After(now.Add(-recentRoomReadWindow)) && now.Before(grant.ExpiresAt) {
			readers = append(readers, recentRoomReader{Actor: protocol.Actor{Kind: actor.Kind, ID: redact.text(actor.ID)}, At: at})
		}
	}
	slices.SortFunc(readers, func(a, b recentRoomReader) int {
		if order := b.At.Compare(a.At); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Actor.Kind, b.Actor.Kind); order != 0 {
			return order
		}
		return cmp.Compare(a.Actor.ID, b.Actor.ID)
	})
	return readers
}

func (c *RoomChat) serveActivity(w http.ResponseWriter, room, token string, settings roomChatGrants) {
	now := time.Now()
	redact := newHistoryRedactor(c.legacy, token)
	online := c.ws.roomPresence.OnlineIDs(room, now)
	for i, id := range online {
		online[i] = redact.text(id)
	}
	writeRoomChatJSON(w, http.StatusOK, struct {
		OnlineAgents []string           `json:"online_agents"`
		RecentReads  []recentRoomReader `json:"recent_reads"`
	}{OnlineAgents: online, RecentReads: c.activity.recent(room, settings, now, redact)})
}
