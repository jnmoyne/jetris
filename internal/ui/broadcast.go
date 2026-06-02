package ui

import "sync"

// Broadcaster fans out updates to multiple subscriber channels.
type Broadcaster[T any] struct {
	mu   sync.RWMutex
	subs map[int]chan T
	next int
}

// NewBroadcaster creates a new broadcaster.
func NewBroadcaster[T any]() *Broadcaster[T] {
	return &Broadcaster[T]{
		subs: make(map[int]chan T),
	}
}

// Subscribe returns a channel that receives updates and a function to unsubscribe.
func (b *Broadcaster[T]) Subscribe() (<-chan T, func()) {
	b.mu.Lock()
	id := b.next
	b.next++
	// Buffer must comfortably hold bursts: a line clear or shrink republishes
	// the entire visible row range (~24 rows) as back-to-back per-row updates,
	// and several such bursts can stack up (multiple players, rapid play).
	// With too small a buffer, Send() (non-blocking) drops the overflow and the
	// board is left with stale, un-repainted rows.
	ch := make(chan T, 1024)
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

// Send broadcasts a value to all subscribers.
func (b *Broadcaster[T]) Send(v T) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- v:
		default:
		}
	}
}

// Close closes all subscriber channels.
func (b *Broadcaster[T]) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, ch := range b.subs {
		close(ch)
		delete(b.subs, id)
	}
}
