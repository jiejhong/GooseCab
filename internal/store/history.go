package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

// HistoryPage is an ordered, room-local snapshot. Sequence numbers are not
// globally unique; SQLite row IDs break ties so a page never silently drops a row.
type HistoryPage struct {
	Events         []protocol.Event
	SnapshotSeq    int64
	SnapshotCursor int64
	Total          int64
	HasMore        bool
	NextCursor     string
}

// History reads its boundary, count and page in one SQLite read transaction.
// A nil through captures the room's current high-water mark. Callers can retain
// both boundaries across pages; later appends above it are excluded.
func (s *Store) History(ctx context.Context, room string, after int64, afterCursor string, through *int64, throughCursor *int64, limit int) (HistoryPage, error) {
	page := HistoryPage{Events: []protocol.Event{}}
	if room == "" || after < 0 || limit < 1 || limit > 10000 || (through != nil && *through < 0) || (throughCursor != nil && *throughCursor < 0) {
		return page, fmt.Errorf("invalid history range")
	}
	cursor := int64(0)
	if afterCursor != "" {
		var err error
		cursor, err = strconv.ParseInt(afterCursor, 10, 64)
		if err != nil || cursor < 1 {
			return page, fmt.Errorf("invalid cursor")
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0),COALESCE(MAX(rowid),0) FROM events WHERE room_id=?`, room).Scan(&page.SnapshotSeq, &page.SnapshotCursor); err != nil {
		return page, err
	}
	if through != nil && *through < page.SnapshotSeq {
		page.SnapshotSeq = *through
	}
	if throughCursor != nil && *throughCursor < page.SnapshotCursor {
		page.SnapshotCursor = *throughCursor
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE room_id=? AND seq<=? AND rowid<=?`, room, page.SnapshotSeq, page.SnapshotCursor).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT rowid,event_id,seq,type,payload,created_at,event_json FROM events
		WHERE room_id=? AND seq<=? AND rowid<=? AND (seq>? OR (seq=? AND ?>0 AND rowid>?))
		ORDER BY seq,rowid LIMIT ?`, room, page.SnapshotSeq, page.SnapshotCursor, after, after, cursor, cursor, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, typ, ts string
		var seq, rowID int64
		var payload, snapshot sql.NullString
		if err = rows.Scan(&rowID, &id, &seq, &typ, &payload, &ts, &snapshot); err != nil {
			return page, err
		}
		var evt protocol.Event
		if snapshot.Valid {
			var decoded *protocol.Event
			decoder := json.NewDecoder(strings.NewReader(snapshot.String))
			decoder.UseNumber()
			if err = decoder.Decode(&decoded); err != nil {
				return page, fmt.Errorf("decode event: %w", err)
			}
			if decoded == nil || decoded.EventID != id || decoded.RoomID != room || decoded.Seq != seq || decoded.Type != typ {
				return page, fmt.Errorf("event snapshot identity mismatch")
			}
			evt = *decoded
		} else {
			var pl any
			if payload.Valid {
				decoder := json.NewDecoder(strings.NewReader(payload.String))
				decoder.UseNumber()
				_ = decoder.Decode(&pl)
			}
			t, _ := time.Parse(time.RFC3339Nano, ts)
			evt = protocol.Event{EventID: id, RoomID: room, Seq: seq, Type: typ, TS: t, Payload: pl}
		}
		page.Events = append(page.Events, evt)
		if len(page.Events) <= limit {
			page.NextCursor = strconv.FormatInt(rowID, 10)
		}
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	page.HasMore = len(page.Events) > limit
	if page.HasMore {
		page.Events = page.Events[:limit]
	}
	return page, tx.Commit()
}
