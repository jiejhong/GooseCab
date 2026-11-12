package eventbus

import (
	"sync"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

type Subscriber chan protocol.Event

type Bus struct {
	mu   sync.Mutex
	subs map[Subscriber]string
}

func New() *Bus { return &Bus{subs: make(map[Subscriber]string)} }

func (b *Bus) Subscribe(buffer int) Subscriber {
	return b.SubscribeRoom("", buffer)
}

// SubscribeRoom avoids letting traffic from unrelated rooms exhaust a
// subscriber's buffer. An empty room subscribes to all events.
func (b *Bus) SubscribeRoom(room string, buffer int) Subscriber {
	ch := make(Subscriber, buffer)
	b.mu.Lock()
	b.subs[ch] = room
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch Subscriber) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
	b.mu.Unlock()
}

func (b *Bus) Publish(evt protocol.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch, room := range b.subs {
		if room != "" && room != evt.RoomID {
			continue
		}
		select {
		case ch <- evt:
		default:
			// Never silently skip an event and then deliver later ones. The
			// consumer must reconnect and replay from its last processed cursor.
			delete(b.subs, ch)
			close(ch)
		}
	}
}
