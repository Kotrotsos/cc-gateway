package ingest

import "sync"

// Broadcaster fans out Written notifications to any number of subscribers (the
// live SSE streams). Publishing never blocks: a subscriber whose buffer is full
// simply misses that event.
type Broadcaster struct {
	mu   sync.Mutex
	subs map[chan Written]struct{}
}

// NewBroadcaster returns an empty broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[chan Written]struct{}{}}
}

// Subscribe registers a new subscriber and returns its channel plus a cancel
// function that unregisters and closes it.
func (b *Broadcaster) Subscribe() (<-chan Written, func()) {
	ch := make(chan Written, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish delivers w to every current subscriber without blocking.
func (b *Broadcaster) Publish(w Written) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- w:
		default:
		}
	}
}
