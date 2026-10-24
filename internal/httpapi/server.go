package httpapi

import (
	"net/http"

	"gpsdemo/internal/aggregator"
	"gpsdemo/internal/control"
	"gpsdemo/internal/events"
)

// Server holds the dependencies needed by HTTP handlers and builds the
// router.
type Server struct {
	Service *aggregator.Service
	Hub     *events.Hub
	Control *control.Proxy // may be nil if no simulators are configured

	MaxBodyBytes      int64
	MaxFixesPerPacket int
	SnapshotMaxBytes  int64
}

// Router builds the complete net/http handler for the aggregator's public
// API (AG-07, AG-08, AG-09), using Go 1.22+ ServeMux wildcard path patterns.
// Patterns are registered without a method prefix; each handler checks
// r.Method itself and responds with the standard JSON error envelope (AG-07)
// on mismatch, rather than relying on ServeMux's plain-text 405 default.
func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/telemetry", s.handleIngest)
	mux.HandleFunc("/api/v1/health", s.handleHealth)

	mux.HandleFunc("/api/v1/vehicles", s.handleListVehicles)
	mux.HandleFunc("/api/v1/vehicles/{id}", s.handleGetVehicle)
	mux.HandleFunc("/api/v1/vehicles/{id}/snapshot", s.handleGetSnapshot)
	mux.HandleFunc("/api/v1/vehicles/{id}/trips", s.handleListTrips)
	mux.HandleFunc("/api/v1/vehicles/{id}/fixes", s.handleListVehicleFixes)

	mux.HandleFunc("/api/v1/trips/{id}", s.handleGetTrip)
	mux.HandleFunc("/api/v1/trips/{id}/fixes", s.handleGetTripFixes)
	mux.HandleFunc("/api/v1/trips/{id}/completeness", s.handleGetCompleteness)
	mux.HandleFunc("/api/v1/trips/{id}/close", s.handleCloseTrip)

	mux.HandleFunc("/api/v1/events", s.handleEventsWebSocket)

	if s.Control != nil {
		mux.HandleFunc("/api/v1/simulators", s.handleListSimulators)
		mux.HandleFunc("/api/v1/simulators/{id}/status", s.handleSimulatorStatus)
		mux.HandleFunc("/api/v1/simulators/{id}/config", s.handleSimulatorConfig)
		mux.HandleFunc("/api/v1/simulators/{id}/{action}", s.handleSimulatorAction)
	}

	return mux
}
