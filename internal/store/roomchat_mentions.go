package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

var ErrMentionReply = errors.New("no received mention for this room member")

type MentionStatus struct {
	Recipient    protocol.Actor `json:"recipient"`
	State        string         `json:"state"`
	ReplyEventID string         `json:"reply_event_id,omitempty"`
}

func (s *Store) EnableTrackedMentions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS room_chat_mentions (
		room_id TEXT NOT NULL, event_id TEXT NOT NULL, sender_kind TEXT NOT NULL, sender TEXT NOT NULL,
		kind TEXT NOT NULL, recipient TEXT NOT NULL, received INTEGER NOT NULL DEFAULT 0, reply_event_id TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(room_id,event_id,kind,recipient)
	)`)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS room_chat_mentions_inbox
		ON room_chat_mentions(room_id,kind,recipient,received)`)
	return err
}

// AppendTrackedRoomChat commits the post and its private receipt rows together.
// A reply advances only the authenticated recipient's row for the original post.
func (s *Store) AppendTrackedRoomChat(ctx context.Context, event *protocol.Event, recipients []protocol.Actor, replyTo string) error {
	return s.appendNext(ctx, event, nil, func(db *sql.Conn, next protocol.Event) error {
		if replyTo != "" {
			result, err := db.ExecContext(ctx, `UPDATE room_chat_mentions SET reply_event_id=?
				WHERE room_id=? AND event_id=? AND kind=? AND recipient=? AND received=1 AND reply_event_id=''`,
				next.EventID, next.RoomID, replyTo, next.Actor.Kind, next.Actor.ID)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return ErrMentionReply
			}
		}
		for _, recipient := range recipients {
			if _, err := db.ExecContext(ctx, `INSERT INTO room_chat_mentions(room_id,event_id,sender_kind,sender,kind,recipient)
				VALUES(?,?,?,?,?,?)`, next.RoomID, next.EventID, next.Actor.Kind, next.Actor.ID, recipient.Kind, recipient.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) PendingMentions(ctx context.Context, room string, actor protocol.Actor, limit int) ([]protocol.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.event_id,e.seq,e.type,e.payload,e.created_at,e.event_json
		FROM room_chat_mentions m JOIN events e ON e.room_id=m.room_id AND e.event_id=m.event_id
		WHERE m.room_id=? AND m.kind=? AND m.recipient=? AND m.received=0 ORDER BY e.seq,e.rowid LIMIT ?`,
		room, actor.Kind, actor.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readEvents(rows, room)
}

func (s *Store) ConfirmMention(ctx context.Context, room string, actor protocol.Actor, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE room_chat_mentions SET received=1
		WHERE room_id=? AND kind=? AND recipient=? AND event_id=?`, room, actor.Kind, actor.ID, id)
	return err
}

func (s *Store) MentionStatuses(ctx context.Context, room string, sender protocol.Actor, id string) ([]MentionStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind,recipient,received,reply_event_id FROM room_chat_mentions
		WHERE room_id=? AND event_id=? AND sender_kind=? AND sender=? ORDER BY kind,recipient`,
		room, id, sender.Kind, sender.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := []MentionStatus{}
	for rows.Next() {
		var status MentionStatus
		var received bool
		if err = rows.Scan(&status.Recipient.Kind, &status.Recipient.ID, &received, &status.ReplyEventID); err != nil {
			return nil, err
		}
		status.State = "pending"
		if status.ReplyEventID != "" {
			status.State = "replied"
		} else if received {
			status.State = "received"
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}
