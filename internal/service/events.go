package service

import (
	"sync"
)

// Hub fans follower changes out to the running streams. The native reader
// reports a change synchronously, so a report must never wait for a slow
// subscriber: every subscription keeps one pending signal and a burst collapses
// into a single refresh.
type Hub struct {
	mu          sync.Mutex
	subscribers map[*Subscription]struct{}
	closed      bool
}

// Subscription is one stream's view of the hub. Close is safe to call twice.
type Subscription struct {
	hub    *Hub
	id     string
	signal chan struct{}
	closed chan struct{}
	once   sync.Once
}

// NewHub builds an empty hub. Its Changed and Reset methods satisfy the
// sessions.Hooks shape, so a native follower can report through it directly.
func NewHub() *Hub {
	return &Hub{subscribers: make(map[*Subscription]struct{})}
}

// Changed reports one changed session identity. A closed hub ignores reports.
func (h *Hub) Changed(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for subscription := range h.subscribers {
		if subscription.id == id {
			subscription.notify()
		}
	}
}

// Reset ends the current stream generation without closing the hub. A native
// disconnect or invalidated follower makes existing snapshots untrustworthy,
// but a later IPC connection must be able to create fresh subscriptions.
func (h *Hub) Reset() {
	h.end(false)
}

// Close ends every current and future subscription. Only the owning service
// lifecycle calls it during terminal shutdown.
func (h *Hub) Close() {
	h.end(true)
}

func (h *Hub) end(terminal bool) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	if terminal {
		h.closed = true
	}
	subscriptions := make([]*Subscription, 0, len(h.subscribers))
	for subscription := range h.subscribers {
		subscriptions = append(subscriptions, subscription)
	}
	clear(h.subscribers)
	h.mu.Unlock()
	for _, subscription := range subscriptions {
		subscription.close()
	}
}

// Subscribe returns one stream's subscription for a single session identity. A
// subscription to an already closed hub is closed immediately.
func (h *Hub) Subscribe(id string) *Subscription {
	subscription := &Subscription{hub: h, id: id, signal: make(chan struct{}, 1), closed: make(chan struct{})}
	h.mu.Lock()
	closed := h.closed
	if !closed {
		h.subscribers[subscription] = struct{}{}
	}
	h.mu.Unlock()
	if closed {
		subscription.close()
	}
	return subscription
}

// Signal reports a coalesced pending change.
func (s *Subscription) Signal() <-chan struct{} { return s.signal }

// Closed reports that no further snapshot can be read.
func (s *Subscription) Closed() <-chan struct{} { return s.closed }

// Close releases the subscription exactly once.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	delete(s.hub.subscribers, s)
	s.hub.mu.Unlock()
	s.close()
}

func (s *Subscription) notify() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

func (s *Subscription) close() {
	s.once.Do(func() { close(s.closed) })
}
