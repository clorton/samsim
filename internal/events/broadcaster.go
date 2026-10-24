// Package events implements the ordered, ephemeral post-commit WebSocket
// event broadcaster (AG-08). It has no persistence or replay: a Broadcaster
// only fans out envelopes to subscribers currently registered at publish
// time, using bounded per-subscriber queues so a slow client cannot block
// ingestion.
package events

// ChangeType enumerates the supported semantic change kinds (AG-08).
type ChangeType string

const (
	ChangeTripCreated         ChangeType = "trip.created"
	ChangeTripUpdated         ChangeType = "trip.updated"
	ChangeTripClosed          ChangeType = "trip.closed"
	ChangeTripsMerged         ChangeType = "trips.merged"
	ChangeVehicleUpdated      ChangeType = "vehicle.updated"
	ChangeTelemetryQuarantine ChangeType = "telemetry.quarantined"
)

// Change is one semantic change within a transaction's envelope. Only the
// fields relevant to Type are populated; JSON marshaling is handled by the
// httpapi layer's DTO mapping, not here, to keep this package transport
// agnostic.
type Change struct {
	Type ChangeType

	// trip.created / trip.updated / trip.closed
	TripID string
	// trip.closed
	CloseReason string

	// trips.merged
	PredecessorTripIDs []string
	SuccessorTripID    string

	// vehicle.updated
	ActiveTripID      *string // nil means no active trip (explicit null)
	LatestTimestampMs *int64

	// telemetry.quarantined
	QuarantinedCount int
}

// Envelope is one WebSocket event frame: one per changed vehicle transaction
// (AG-08).
type Envelope struct {
	EventID     string
	DatabaseID  string
	VehicleID   string
	Revision    int64
	Type        string // always "vehicle.changed" for now
	TimestampMs int64
	Changes     []Change
}

// ReadyFrame is the first frame sent to a new subscriber, registered under
// the same writer coordinator lock as commits so that no commit can be
// missed between computing the ready revision and registering the
// subscription (AG-08).
type ReadyFrame struct {
	DatabaseID string
	VehicleID  string
	Revision   int64
}
