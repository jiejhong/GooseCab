package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func TestInvocationEventsIndexedAndBounded(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	large := strings.Repeat("x", 8192)
	for i := 0; i < 5000; i++ {
		e := protocol.Event{EventID: fmt.Sprintf("chat-%d", i), RoomID: "busy", Seq: int64(i + 1), Type: "chat.message", Payload: large}
		if err = s.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	e := protocol.Event{RoomID: "busy", Type: "capability.invoke", CorrelationID: "r", Payload: map[string]any{"request_id": "r"}}
	if err = s.AppendNext(ctx, &e); err != nil {
		t.Fatal(err)
	}
	events, err := s.InvocationEvents(ctx, "busy", "r")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	// Check the plan rather than a hardware-dependent wall-clock assertion.
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT event_id FROM events WHERE room_id='busy' AND correlation_id='r' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	indexed := false
	for rows.Next() {
		var a, b, c int
		var detail string
		if err = rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "idx_events_room_correlation_seq") {
			indexed = true
		}
	}
	rows.Close()
	if !indexed {
		t.Fatal("invocation lookup did not use correlation index")
	}
	for i := 0; i < 2048; i++ {
		e := protocol.Event{RoomID: "busy", Type: "capability.progress", CorrelationID: "r"}
		if err = s.AppendNext(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.InvocationEvents(ctx, "busy", "r"); err == nil {
		t.Fatal("unbounded duplicate history accepted")
	}
}

func TestInvocationIndexMigratesExistingSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE events(event_id TEXT PRIMARY KEY,room_id TEXT NOT NULL,seq INTEGER NOT NULL,type TEXT NOT NULL,payload TEXT,created_at TEXT NOT NULL,event_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	event := protocol.Event{EventID: "legacy", RoomID: "room", Seq: 1, Type: "capability.invoke", CorrelationID: "r", Payload: map[string]any{"request_id": "r"}}
	raw, _ := json.Marshal(event)
	if _, err = db.Exec(`INSERT INTO events VALUES('legacy','room',1,'capability.invoke','{}','2026-01-01',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	events, err := s.InvocationEvents(context.Background(), "room", "r")
	if err != nil || len(events) != 1 || events[0].CorrelationID != "r" {
		t.Fatalf("migration lost invocation: %#v %v", events, err)
	}
	if _, err = s.db.Exec(`UPDATE events SET event_json='corrupt' WHERE event_id='legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InvocationEvents(context.Background(), "room", "r"); err == nil {
		t.Fatal("corrupt correlated snapshot did not fail closed")
	}
}
