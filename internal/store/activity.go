package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

// LatestRoomChatActivity returns the time of the newest room-ordered chat with
// visible text and a usable persisted timestamp. A nil result is a known empty
// result, including for legacy rooms without any qualifying events.
func (s *Store) LatestRoomChatActivity(ctx context.Context, room string) (*time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT created_at,payload FROM events
		WHERE room_id=? AND type IN ('chat.message','chat') AND payload IS NOT NULL
		ORDER BY seq DESC,rowid DESC`, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var createdAt, payload string
		if err := rows.Scan(&createdAt, &payload); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil || at.IsZero() || at.After(now) || !hasChatText(payload) {
			continue
		}
		utc := at.UTC()
		return &utc, nil
	}
	return nil, rows.Err()
}

func hasChatText(raw string) bool {
	var payload any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return false
	}
	return protocol.HasChatText(payload)
}
