package eventbus

import (
	"sync"

	"github.com/jiejhong/GooseCab/internal/protocol"
)

type Subscriber chan protocol.Event

type Bus struct {
	mu   sync.RWMutex
	subs map[Subscriber]struct{}
}

func New() *Bus { return &Bus{subs: make(map[Subscriber]struct{})} }

func (b *Bus) Subscribe(buffer int) Subscriber {
	ch := make(Subscriber, buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
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
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}
