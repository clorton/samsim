package aggregator

// IngestResult is both the HTTP response body for successful ingestion and
// the value persisted as packets.stored_result_json for replay (AG-04).
type IngestResult struct {
	SimulatorID      string `json:"simulator_id"`
	PacketID         string `json:"packet_id"`
	DuplicatePacket  bool   `json:"duplicate_packet"`
	AcceptedFixes    int    `json:"accepted_fixes"`
	DuplicateFixes   int    `json:"duplicate_fixes"`
	QuarantinedFixes int    `json:"quarantined_fixes"`
	TripUpdates      int    `json:"trip_updates"`
	Revision         int64  `json:"revision"`
}
