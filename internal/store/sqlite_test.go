package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func completeEvent(id, room string, seq int64) protocol.Event {
	return protocol.Event{
		EventID: id, RoomID: room, Seq: seq, Type: "capability.result",
		Actor:  protocol.Actor{Kind: "agent", ID: "worker"},
		TaskID: "task-1", CapabilityID: "cap.echo", SessionID: "session-1",
		CorrelationID: "request-1", CausationID: "invoke-1",
		TS: time.Date(2026, time.November, 10, 1, 2, 3, 123456789, time.UTC),
		Payload: map[string]any{
			"request_id": "request-1", "status": "completed",
			"output_ref": "file:///worker/out.json",
		},
	}
}

func assertEventEqual(t *testing.T, got, want protocol.Event) {
	t.Helper()
	// Compare protocol JSON, independent of the Go type used for Payload.
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("replayed event differs:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestEventReplayPreservesCompleteEnvelopeAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.Event{
		completeEvent("result-1", "demo", 1),
		completeEvent("result-2", "demo", 2),
	}
	for _, evt := range append(want, completeEvent("other-room", "other", 1)) {
		if err = s.Append(ctx, evt); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.EventsAfter(ctx, "demo", 0)
	if err != nil || len(got) != len(want) {
		t.Fatalf("EventsAfter: got %d events, err %v", len(got), err)
	}
	for i := range want {
		assertEventEqual(t, got[i], want[i])
	}
	got, err = s.EventsAfter(ctx, "demo", 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("resume: got %d events, err %v", len(got), err)
	}
	assertEventEqual(t, got[0], want[1])
	if next, err := s.NextSeq(ctx, "demo"); err != nil || next != 3 {
		t.Fatalf("NextSeq = %d, %v; want 3", next, err)
	}
}

func TestOpenMigratesLegacyDatabaseWithoutInventingMetadata(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE events (event_id TEXT PRIMARY KEY, room_id TEXT NOT NULL, seq INTEGER NOT NULL, type TEXT NOT NULL, payload TEXT, created_at TEXT NOT NULL);
		INSERT INTO events VALUES('legacy-1','demo',1,'capability.result','{"request_id":"old-request","status":"completed"}','2026-11-09T00:00:00Z');
		INSERT INTO events VALUES('legacy-null','demo',2,'note',NULL,'2026-11-09T00:00:01Z');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening twice verifies migration is idempotent and mixed old/new rows
	// remain readable even though only the new row has a complete envelope.
	want := completeEvent("new-3", "demo", 3)
	for attempt := 0; attempt < 2; attempt++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			if err = s.Append(ctx, want); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.EventsAfter(ctx, "demo", 0)
		if err != nil || len(got) != 3 {
			t.Fatalf("legacy replay: got %d events, err %v", len(got), err)
		}
		legacy := protocol.Event{
			EventID: "legacy-1", RoomID: "demo", Seq: 1, Type: "capability.result",
			TS:      time.Date(2026, time.November, 9, 0, 0, 0, 0, time.UTC),
			Payload: map[string]any{"request_id": "old-request", "status": "completed"},
		}
		assertEventEqual(t, got[0], legacy)
		if got[1].EventID != "legacy-null" || got[1].Payload != nil {
			t.Fatalf("legacy NULL payload changed: %+v", got[1])
		}
		assertEventEqual(t, got[2], want)
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAppendRejectsUnencodableEventWithoutWriting(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evt := completeEvent("invalid", "demo", 1)
	evt.Payload = make(chan int)
	if err = s.Append(context.Background(), evt); err == nil {
		t.Fatal("Append accepted an unencodable payload")
	}
	got, err := s.EventsAfter(context.Background(), "demo", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("invalid event was stored: %v, %v", got, err)
	}
}

func TestReplayReportsCorruptSnapshot(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evt := completeEvent("corrupt", "demo", 1)
	if err = s.Append(context.Background(), evt); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []string{"{", "null", "{}"} {
		if _, err = s.db.Exec(`UPDATE events SET event_json=? WHERE event_id='corrupt'`, snapshot); err != nil {
			t.Fatal(err)
		}
		if _, err = s.EventsAfter(context.Background(), "demo", 0); err == nil {
			t.Fatalf("corrupt snapshot %q silently lost envelope metadata", snapshot)
		}
	}
}

func TestEventPayloadRoundTrip(t *testing.T) {
	for _, payload := range []any{nil, "text", []any{"item", true}, map[string]any{"percent": float64(50)}} {
		t.Run(string(mustJSON(t, payload)), func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			evt := completeEvent("payload", "demo", 1)
			evt.Payload = payload
			if err = s.Append(context.Background(), evt); err != nil {
				t.Fatal(err)
			}
			got, err := s.EventsAfter(context.Background(), "demo", 0)
			if err != nil || len(got) != 1 {
				t.Fatalf("replay: got %d events, err %v", len(got), err)
			}
			assertEventEqual(t, got[0], evt)
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
