package events

import "sync"

// Hub tracks per-vehicle subscriber sets and delivers envelopes
// nonblockingly. Publish must be called only from within the writer
// coordinator, in commit order, so that delivery order matches commit
// order (AG-01, AG-08). Hub itself does no network I/O.
type Hub struct {
	queueCapacity int

	mu        sync.Mutex
	nextID    uint64
	byVehicle map[string]map[uint64]*Subscriber
}

// NewHub returns a Hub whose subscriber channels have the given bounded
// capacity (config websocket_queue_capacity).
func NewHub(queueCapacity int) *Hub {
	if queueCapacity <= 0 {
		queueCapacity = 1
	}
	return &Hub{
		queueCapacity: queueCapacity,
		byVehicle:     make(map[string]map[uint64]*Subscriber),
	}
}

// Subscribe registers a new subscriber for vehicleID. Callers must call this
// under the same writer-coordinator lock used for commits, immediately
// before/while computing the ready revision, so the ready frame they send
// afterward is guaranteed to precede delivery of any later commit's
// envelope (AG-08).
func (h *Hub) Subscribe(vehicleID string) *Subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	sub := &Subscriber{
		id:        h.nextID,
		vehicleID: vehicleID,
		ch:        make(chan Envelope, h.queueCapacity),
	}
	set, ok := h.byVehicle[vehicleID]
	if !ok {
		set = make(map[uint64]*Subscriber)
		h.byVehicle[vehicleID] = set
	}
	set[sub.id] = sub
	return sub
}

// Unsubscribe removes sub from its vehicle's set and closes its channel if
// not already closed. Safe to call multiple times.
func (h *Hub) Unsubscribe(sub *Subscriber) {
	h.mu.Lock()
	if set, ok := h.byVehicle[sub.vehicleID]; ok {
		delete(set, sub.id)
		if len(set) == 0 {
			delete(h.byVehicle, sub.vehicleID)
		}
	}
	h.mu.Unlock()
	sub.close()
}

// Publish delivers env to every current subscriber of env.VehicleID. A
// subscriber whose queue is full is marked overflowed, unsubscribed, and
// its channel closed; the HTTP layer observes this via Subscriber.Overflowed
// after the channel closes and closes the socket with code 1013. Publish
// never blocks.
func (h *Hub) Publish(env Envelope) {
	h.mu.Lock()
	set, ok := h.byVehicle[env.VehicleID]
	if !ok || len(set) == 0 {
		h.mu.Unlock()
		return
	}
	subs := make([]*Subscriber, 0, len(set))
	for _, s := range set {
		subs = append(subs, s)
	}
	h.mu.Unlock()

	for _, sub := range subs {
		select {
		case sub.ch <- env:
		default:
			h.removeFromIndex(sub)
			sub.markOverflowedAndClose()
		}
	}
}

// removeFromIndex removes sub from the vehicle index without touching its
// channel; the caller is responsible for closing it.
func (h *Hub) removeFromIndex(sub *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set, ok := h.byVehicle[sub.vehicleID]; ok {
		delete(set, sub.id)
		if len(set) == 0 {
			delete(h.byVehicle, sub.vehicleID)
		}
	}
}
