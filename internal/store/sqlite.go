package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL`); err == nil {
		err = migrate(db)
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db}, nil
}

// migrate keeps the original columns readable and adds a complete event
// snapshot. NULL snapshots identify legacy rows; metadata never stored by old
// versions cannot be reconstructed reliably and is left unset when replayed.
func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS events (
		event_id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		type TEXT NOT NULL,
		payload TEXT,
		created_at TEXT NOT NULL,
		event_json TEXT
	)`); err != nil {
		return err
	}
	rows, err := tx.Query(`PRAGMA table_info(events)`)
	if err != nil {
		return err
	}
	hasEventJSON := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "event_json" {
			hasEventJSON = true
		}
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if !hasEventJSON {
		if _, err = tx.Exec(`ALTER TABLE events ADD COLUMN event_json TEXT`); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_events_room_seq ON events(room_id,seq)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Append(ctx context.Context, e protocol.Event) error {
	snapshot, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event %q: %w", e.EventID, err)
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("encode event %q payload: %w", e.EventID, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO events(event_id,room_id,seq,type,payload,created_at,event_json) VALUES(?,?,?,?,?,?,?)`,
		e.EventID, e.RoomID, e.Seq, e.Type, string(payload), e.TS.UTC().Format(time.RFC3339Nano), string(snapshot))
	return err
}

func (s *Store) EventsAfter(ctx context.Context, room string, after int64) ([]protocol.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,seq,type,payload,created_at,event_json FROM events WHERE room_id=? AND seq>? ORDER BY seq`, room, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Event
	for rows.Next() {
		var id, typ, ts string
		var payload, snapshot sql.NullString
		var seq int64
		if err = rows.Scan(&id, &seq, &typ, &payload, &ts, &snapshot); err != nil {
			return nil, err
		}
		var evt protocol.Event
		if snapshot.Valid {
			var decoded *protocol.Event
			if err = json.Unmarshal([]byte(snapshot.String), &decoded); err != nil {
				return nil, fmt.Errorf("decode event %q: %w", id, err)
			}
			if decoded == nil || decoded.EventID != id || decoded.RoomID != room || decoded.Seq != seq || decoded.Type != typ {
				return nil, fmt.Errorf("event %q snapshot does not match stored identity", id)
			}
			evt = *decoded
		} else {
			// Preserve the original replay behavior for existing rows, including
			// empty payloads produced by older writers. Do not invent missing
			// actors or correlation fields from unrelated payload keys.
			var pl any
			if payload.Valid {
				_ = json.Unmarshal([]byte(payload.String), &pl)
			}
			t, _ := time.Parse(time.RFC3339Nano, ts)
			evt = protocol.Event{EventID: id, RoomID: room, Seq: seq, Type: typ, TS: t, Payload: pl}
		}
		out = append(out, evt)
	}
	return out, rows.Err()
}

func (s *Store) NextSeq(ctx context.Context, room string) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM events WHERE room_id=?`, room).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n.Int64 + 1, nil
}

func (s *Store) String() string { return "sqlite store" }
