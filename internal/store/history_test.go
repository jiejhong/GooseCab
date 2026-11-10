package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryLegacyAndLargeNumbers(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.db.Exec(`INSERT INTO events(event_id,room_id,seq,type,payload,created_at) VALUES('legacy','demo',1,'chat.message','{"text":"old","large":9007199254740993}','2026-11-09T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.History(context.Background(), "demo", 0, "", nil, nil, 10)
	if err != nil || len(p.Events) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if got := string(mustJSON(t, p.Events[0])); !strings.Contains(got, "9007199254740993") || p.Events[0].Actor.ID != "" {
		t.Fatal(got)
	}
}

func TestHistoryBoundedReadAndIntegrity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10001; i++ {
		_, err = tx.Exec(`INSERT INTO events(event_id,room_id,seq,type,payload,created_at) VALUES(?,'demo',?,'note','{}','2026-11-10T00:00:00Z')`, fmt.Sprint(i), i)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	p, err := s.History(context.Background(), "demo", 0, "", nil, nil, 10000)
	if err != nil || !p.HasMore || len(p.Events) != 10000 || p.Total != 10001 {
		t.Fatalf("bad bound %d %d %v %v", len(p.Events), p.Total, p.HasMore, err)
	}
	_, err = s.db.Exec(`UPDATE events SET event_json='{}' WHERE event_id='1'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.History(context.Background(), "demo", 0, "", nil, nil, 10); err == nil {
		t.Fatal("silently accepted corrupt snapshot")
	}
}

func TestHistorySnapshotExcludesLateRowsWithEarlierSequence(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := int64(1); i <= 3; i++ {
		if err = s.Append(context.Background(), completeEvent(fmt.Sprint(i), "demo", i)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.History(context.Background(), "demo", 0, "", nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Append(context.Background(), completeEvent("late", "demo", 2)); err != nil {
		t.Fatal(err)
	}
	next, err := s.History(context.Background(), "demo", 1, first.NextCursor, &first.SnapshotSeq, &first.SnapshotCursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if next.Total != 3 || len(next.Events) != 2 || next.Events[0].EventID != "2" || next.Events[1].EventID != "3" {
		t.Fatalf("snapshot changed: %+v", next)
	}
}
