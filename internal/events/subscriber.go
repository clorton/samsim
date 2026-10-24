package events

import "sync"

// Subscriber receives envelopes for one vehicle via a bounded channel. If
// the channel fills (a slow client), the broadcaster marks the subscriber
// Overflowed and stops delivering; the HTTP layer must then close the
// socket with code 1013, per AG-08 ("never silently drop an event and
// continue").
type Subscriber struct {
	id        uint64
	vehicleID string
	ch        chan Envelope

	mu         sync.Mutex
	overflowed bool
	closed     bool
}

// Envelopes returns the channel on which envelopes are delivered. It is
// closed when the subscriber is unsubscribed or overflows.
func (s *Subscriber) Envelopes() <-chan Envelope { return s.ch }

// Overflowed reports whether this subscriber's queue overflowed and it was
// dropped by the broadcaster.
func (s *Subscriber) Overflowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overflowed
}

func (s *Subscriber) markOverflowedAndClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.overflowed = true
	s.closed = true
	close(s.ch)
}

func (s *Subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}
