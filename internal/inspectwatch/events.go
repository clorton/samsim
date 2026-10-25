package inspectwatch

import "gpsdemo/internal/inspectclient"

// EventKind enumerates the watch state machine's observable outputs,
// independent of how the command layer renders them (human text or NDJSON
// records), per INS-04 section 7's documented record types.
type EventKind string

const (
	EventSnapshot     EventKind = "snapshot"
	EventEnvelope     EventKind = "event"
	EventResync       EventKind = "resync"
	EventWaiting      EventKind = "waiting"
	EventDisconnected EventKind = "disconnected"
	EventError        EventKind = "error"
)

// Event is one state-machine output. Only the fields relevant to Kind are
// populated.
type Event struct {
	Kind EventKind

	DatabaseID string
	VehicleID  string
	Revision   int64

	Snapshot *inspectclient.SnapshotDTO
	Envelope *EnvelopeData

	Reason  string // resync
	Message string // waiting / disconnected / error
	Attempt int    // disconnected: which reconnect attempt this is (1-based)
}

// EnvelopeData is a transport-agnostic copy of one semantic-change
// envelope. JSON field names mirror AG-08's own envelope shape so the
// NDJSON "event" record's data field matches the documented wire contract.
type EnvelopeData struct {
	EventID     string       `json:"event_id"`
	Type        string       `json:"type"`
	TimestampMs int64        `json:"timestamp_ms"`
	Changes     []ChangeData `json:"changes"`
}

// ChangeData is a transport-agnostic copy of one envelope change entry.
type ChangeData struct {
	Type               string   `json:"type"`
	TripID             string   `json:"trip_id,omitempty"`
	CloseReason        string   `json:"close_reason,omitempty"`
	PredecessorTripIDs []string `json:"predecessor_trip_ids,omitempty"`
	SuccessorTripID    string   `json:"successor_trip_id,omitempty"`
	ActiveTripID       *string  `json:"active_trip_id,omitempty"`
	LatestTimestampMs  *int64   `json:"latest_timestamp_ms,omitempty"`
	QuarantinedCount   int      `json:"quarantined_count,omitempty"`
}

func toEnvelopeData(f envelopeFrame) *EnvelopeData {
	ed := &EnvelopeData{EventID: f.EventID, Type: f.Type, TimestampMs: f.TimestampMs}
	for _, c := range f.Changes {
		ed.Changes = append(ed.Changes, ChangeData{
			Type:               c.Type,
			TripID:             c.TripID,
			CloseReason:        c.CloseReason,
			PredecessorTripIDs: c.PredecessorTripIDs,
			SuccessorTripID:    c.SuccessorTripID,
			ActiveTripID:       c.ActiveTripID,
			LatestTimestampMs:  c.LatestTimestampMs,
			QuarantinedCount:   c.QuarantinedCount,
		})
	}
	return ed
}
