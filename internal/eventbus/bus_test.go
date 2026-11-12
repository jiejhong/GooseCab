package eventbus

import (
	"sync"
	"testing"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

func TestOverflowClosesInsteadOfSilentlySkipping(t *testing.T) {
	b := New()
	slow := b.SubscribeRoom("room", 1)
	fast := b.SubscribeRoom("room", 4)
	for seq := int64(1); seq <= 3; seq++ {
		b.Publish(protocol.Event{RoomID: "room", Seq: seq})
	}
	if got := <-slow; got.Seq != 1 {
		t.Fatalf("buffered event = %d, want 1", got.Seq)
	}
	if _, ok := <-slow; ok {
		t.Fatal("overflow subscriber remained open")
	}
	for seq := int64(1); seq <= 3; seq++ {
		if got := <-fast; got.Seq != seq {
			t.Fatalf("fast subscriber event = %d, want %d", got.Seq, seq)
		}
	}
	b.Unsubscribe(slow) // Safe even after overflow removed/closed it.
	b.Unsubscribe(slow)
	b.Unsubscribe(fast)
}

func TestRoomSubscriptionCannotBeOverflowedByOtherRooms(t *testing.T) {
	b := New()
	sub := b.SubscribeRoom("quiet", 1)
	defer b.Unsubscribe(sub)
	for i := 0; i < 1000; i++ {
		b.Publish(protocol.Event{RoomID: "busy", Seq: int64(i + 1)})
	}
	b.Publish(protocol.Event{RoomID: "quiet", Seq: 1})
	if got, ok := <-sub; !ok || got.RoomID != "quiet" || got.Seq != 1 {
		t.Fatalf("quiet room affected by unrelated traffic: %#v, open=%v", got, ok)
	}
}

func TestConcurrentPublishUnsubscribeAndOverflow(t *testing.T) {
	b := New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				sub := b.Subscribe(1)
				b.Publish(protocol.Event{RoomID: "room"})
				b.Publish(protocol.Event{RoomID: "room"})
				b.Unsubscribe(sub)
			}
		}()
	}
	wg.Wait()
}
