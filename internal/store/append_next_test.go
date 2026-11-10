package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jiejhong/GooseCab/internal/p1"
	"github.com/jiejhong/GooseCab/internal/p2"
	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/store"
	"github.com/jiejhong/GooseCab/internal/transfer"
)

func TestAppendNextConcurrentStoresAndProducers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	first, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	invocation := p1.New(first)
	plans := p2.New(second, nil)
	transfers := transfer.New(second, nil)
	var wg sync.WaitGroup
	errors := make(chan error, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprint(i)
			room := []string{"", "alpha", "beta"}[i%3]
			var err error
			switch i % 4 {
			case 0:
				e := protocol.Event{RoomID: room, Type: "shared.type"}
				err = first.AppendNext(ctx, &e)
			case 1:
				_, err = invocation.CreateInvocation(ctx, p1.Invocation{ID: id, RoomID: room})
			case 2:
				_, err = transfers.Save(ctx, transfer.Metadata{ID: id, RoomID: room}, "shared.type")
			case 3:
				_, err = plans.CreatePlan(ctx, p2.Plan{ID: id})
			}
			if err != nil {
				errors <- err
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	seen := map[string]bool{}
	count := 0
	for _, room := range []string{"", "alpha", "beta", "transfers"} {
		events, err := first.EventsAfter(ctx, room, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i, e := range events {
			if e.Seq != int64(i+1) {
				t.Errorf("room %q sequence %d at index %d", room, e.Seq, i)
			}
			if e.EventID == "" || seen[e.EventID] {
				t.Errorf("missing/duplicate ID %q", e.EventID)
			}
			if e.TS.IsZero() {
				t.Error("missing timestamp")
			}
			seen[e.EventID] = true
			count++
		}
	}
	if count != 200 {
		t.Fatalf("stored %d events, want 200", count)
	}
}

func TestAppendNextFailureAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	timestamp := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	initial := protocol.Event{EventID: "explicit", RoomID: "room", Seq: 42, Type: "original", TS: timestamp}
	if err = s.Append(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	duplicate := protocol.Event{EventID: "explicit", RoomID: "room", Type: "duplicate"}
	if err = s.AppendNext(ctx, &duplicate); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	if duplicate.Seq != 0 || !duplicate.TS.IsZero() {
		t.Fatal("failed write changed caller event")
	}
	invalid := protocol.Event{RoomID: "room", Payload: make(chan int)}
	if err = s.AppendNext(ctx, &invalid); err == nil {
		t.Fatal("invalid payload accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = s.AppendNext(canceled, &invalid); err == nil {
		t.Fatal("canceled write succeeded")
	}
	next := protocol.Event{RoomID: "room", Type: "next", TS: timestamp}
	if err = s.AppendNext(ctx, &next); err != nil {
		t.Fatal(err)
	}
	if next.Seq != 43 || next.EventID == "" || !next.TS.Equal(timestamp) {
		t.Fatalf("incorrect allocation: %+v", next)
	}
	events, err := s.EventsAfter(ctx, "room", 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("replay: %v %v", events, err)
	}
	if events[1].EventID != next.EventID || events[1].Seq != next.Seq {
		t.Fatal("persisted identity differs")
	}
	if err = s.AppendNext(ctx, nil); err == nil {
		t.Fatal("nil event accepted")
	}
}
