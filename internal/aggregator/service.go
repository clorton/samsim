// Package aggregator implements the transactional services that tie
// together wire decoding (internal/httpapi), pure trip reconciliation
// (internal/trips), persistence (internal/storage) and event publication
// (internal/events): packet ingestion (AG-04), trip reconstruction (AG-05),
// and manual closure (AG-06).
package aggregator

import (
	"sync"

	"gpsdemo/internal/clock"
	"gpsdemo/internal/events"
	"gpsdemo/internal/idgen"
	"gpsdemo/internal/storage"
)

// Service is the aggregator's transactional domain service. One Service is
// shared by all HTTP handlers and holds the per-vehicle lock table required
// by AG-01's locking discipline (vehicle lock acquired before the writer
// coordinator).
type Service struct {
	Store *storage.Store
	Hub   *events.Hub
	Clock clock.Clock
	IDGen idgen.Generator

	MaxFixesPerPacket int

	vehicleLocksMu sync.Mutex
	vehicleLocks   map[string]*sync.Mutex
}

// NewService constructs a Service. maxFixesPerPacket must match the
// aggregator's configured limit.
func NewService(store *storage.Store, hub *events.Hub, clk clock.Clock, idGen idgen.Generator, maxFixesPerPacket int) *Service {
	return &Service{
		Store:             store,
		Hub:               hub,
		Clock:             clk,
		IDGen:             idGen,
		MaxFixesPerPacket: maxFixesPerPacket,
		vehicleLocks:      make(map[string]*sync.Mutex),
	}
}

// lockVehicle acquires the per-vehicle lock for vehicleID, creating it if
// necessary, and returns a function to release it. Per AG-01, this must
// always be acquired before the storage writer coordinator.
func (s *Service) lockVehicle(vehicleID string) func() {
	s.vehicleLocksMu.Lock()
	l, ok := s.vehicleLocks[vehicleID]
	if !ok {
		l = &sync.Mutex{}
		s.vehicleLocks[vehicleID] = l
	}
	s.vehicleLocksMu.Unlock()

	l.Lock()
	return l.Unlock
}
