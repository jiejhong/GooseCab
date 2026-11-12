package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jiejhong/GooseCab/internal/protocol"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection also keeps :memory: stores on the same database.
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=5000`); err == nil {
		_, err = db.Exec(`PRAGMA journal_mode=WAL`)
	}
	if err == nil {
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
	hasCorrelationID := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "correlation_id" {
			hasCorrelationID = true
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
	if !hasCorrelationID {
		if _, err = tx.Exec(`ALTER TABLE events ADD COLUMN correlation_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE events SET correlation_id=COALESCE(json_extract(event_json,'$.correlation_id'),'') WHERE json_valid(event_json)`); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_events_room_correlation_seq ON events(room_id,correlation_id,seq)`); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_events_room_seq ON events(room_id,seq)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

// AppendNext atomically allocates a room sequence and persists the complete event.
// BEGIN IMMEDIATE serializes writers even when they use different Store instances
// pointing at the same database. The caller's event changes only after commit.
func (s *Store) AppendNext(ctx context.Context, e *protocol.Event) error {
	if e == nil {
		return fmt.Errorf("append event: nil event")
	}
	next := *e
	if next.EventID == "" {
		next.EventID = uuid.NewString()
	}
	if next.TS.IsZero() {
		next.TS = time.Now().UTC()
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Configure the checked-out connection as SQLite pragmas are connection-local.
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	if err = conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE room_id=?`, next.RoomID).Scan(&next.Seq); err != nil {
		return err
	}
	if err = appendEvent(ctx, conn, next); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	*e = next
	return nil
}

// Append preserves caller-supplied sequence numbers for imports. Live producers
// must use AppendNext rather than combining NextSeq and Append.
func (s *Store) Append(ctx context.Context, e protocol.Event) error {
	return appendEvent(ctx, s.db, e)
}

type eventExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendEvent(ctx context.Context, db eventExecer, e protocol.Event) error {
	snapshot, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event %q: %w", e.EventID, err)
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("encode event %q payload: %w", e.EventID, err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO events(event_id,room_id,seq,type,payload,created_at,event_json,correlation_id) VALUES(?,?,?,?,?,?,?,?)`,
		e.EventID, e.RoomID, e.Seq, e.Type, string(payload), e.TS.UTC().Format(time.RFC3339Nano), string(snapshot), e.CorrelationID)
	return err
}

func (s *Store) EventsAfter(ctx context.Context, room string, after int64) ([]protocol.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,seq,type,payload,created_at,event_json FROM events WHERE room_id=? AND seq>? ORDER BY seq`, room, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readEvents(rows, room)
}

func readEvents(rows *sql.Rows, room string) ([]protocol.Event, error) {
	var err error
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

// NextSeq returns a snapshot only; it does not reserve a sequence.
// Use AppendNext to allocate and persist an event atomically.
func (s *Store) NextSeq(ctx context.Context, room string) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM events WHERE room_id=?`, room).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n.Int64 + 1, nil
}

func (s *Store) String() string { return "sqlite store" }

const MaxInvocationEvents = 2048

// InvocationEvents uses the room/correlation index, not full room replay. It
// bounds even pathological duplicate histories and fails closed at the limit.
func (s *Store) InvocationEvents(ctx context.Context, room, id string) ([]protocol.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,seq,type,payload,created_at,event_json FROM events
 WHERE room_id=? AND correlation_id=? AND type IN ('capability.invoke','capability.progress','capability.result') ORDER BY seq LIMIT ?`, room, id, MaxInvocationEvents+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events, err := readEvents(rows, room)
	if err != nil {
		return nil, err
	}
	if len(events) > MaxInvocationEvents {
		return nil, fmt.Errorf("invocation history exceeds safety limit")
	}
	return events, nil
}
