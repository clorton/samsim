package inspectwatch

// readyFrame mirrors AG-08's first application frame.
type readyFrame struct {
	Type       string `json:"type"`
	DatabaseID string `json:"database_id"`
	VehicleID  string `json:"vehicle_id"`
	Revision   int64  `json:"revision"`
}

// changeFrame mirrors one entry of an envelope's "changes" array.
type changeFrame struct {
	Type               string   `json:"type"`
	TripID             string   `json:"trip_id,omitempty"`
	CloseReason        string   `json:"close_reason,omitempty"`
	PredecessorTripIDs []string `json:"predecessor_trip_ids,omitempty"`
	SuccessorTripID    string   `json:"successor_trip_id,omitempty"`
	ActiveTripID       *string  `json:"active_trip_id,omitempty"`
	LatestTimestampMs  *int64   `json:"latest_timestamp_ms,omitempty"`
	QuarantinedCount   int      `json:"quarantined_count,omitempty"`
}

// envelopeFrame mirrors AG-08's subsequent application frames.
type envelopeFrame struct {
	EventID     string        `json:"event_id"`
	DatabaseID  string        `json:"database_id"`
	VehicleID   string        `json:"vehicle_id"`
	Revision    int64         `json:"revision"`
	Type        string        `json:"type"`
	TimestampMs int64         `json:"timestamp_ms"`
	Changes     []changeFrame `json:"changes"`
}
