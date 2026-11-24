package store

import (
	"context"
	"database/sql"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

// EnableRoomChat is additive. It is called only when the Gateway opts in.
func (s *Store) EnableRoomChat(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS room_chat_unread (
		room_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		recipient TEXT NOT NULL,
		event_id TEXT NOT NULL,
		PRIMARY KEY(room_id,kind,recipient,event_id)
	)`)
	return err
}

func (s *Store) AppendRoomChat(ctx context.Context, event *protocol.Event, recipients []protocol.Actor) error {
	return s.appendNext(ctx, event, recipients)
}

func (s *Store) RoomChatUnread(ctx context.Context, room string, actor protocol.Actor, limit int) ([]protocol.Event, int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	const from = ` FROM room_chat_unread u JOIN events e ON e.event_id=u.event_id AND e.room_id=u.room_id
		WHERE u.room_id=? AND u.kind=? AND u.recipient=?`
	args := []any{room, actor.Kind, actor.ID}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)`+from, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.event_id,e.seq,e.type,e.payload,e.created_at,e.event_json`+
		from+` ORDER BY e.seq DESC,e.rowid DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events, err := readEvents(rows, room)
	if err != nil {
		return nil, 0, err
	}
	if events == nil {
		events = []protocol.Event{}
	}
	return events, count, tx.Commit()
}

func (s *Store) RoomChatPending(ctx context.Context, room string, actor protocol.Actor, events []protocol.Event) ([]string, error) {
	if len(events) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var pending []string
	for _, event := range events {
		var found int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM room_chat_unread WHERE room_id=? AND kind=? AND recipient=? AND event_id=?`,
			room, actor.Kind, actor.ID, event.EventID).Scan(&found)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		pending = append(pending, event.EventID)
	}
	return pending, tx.Commit()
}

func (s *Store) AcknowledgeRoomChat(ctx context.Context, room string, actor protocol.Actor, events []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range events {
		if _, err := tx.ExecContext(ctx, `DELETE FROM room_chat_unread WHERE room_id=? AND kind=? AND recipient=? AND event_id=?`,
			room, actor.Kind, actor.ID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
