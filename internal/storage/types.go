package storage

// VehicleRow mirrors the vehicles table.
type VehicleRow struct {
	VehicleID            string
	LatestFixTimestampMs *int64
	ActiveTripID         *string
	Revision             int64
	UpdatedAtMs          int64
}

// FixRow mirrors the gps_fixes table.
type FixRow struct {
	VehicleID        string
	TimestampMs      int64
	Latitude         float64
	Longitude        float64
	SpeedMps         float64
	HeadingDegrees   float64
	FirstSimulatorID string
	FirstPacketID    string
	ReceivedAtMs     int64
	AssignmentReason *string // "MANUAL_CLOSED_INTERVAL" or nil
}

// Trip status and close reason constants (AG-03 CHECK constraints).
const (
	TripStatusActive   = "ACTIVE"
	TripStatusInactive = "INACTIVE"
	TripStatusClosed   = "CLOSED"

	CloseReasonNone   = "NONE"
	CloseReasonManual = "MANUAL"
	CloseReasonMerged = "MERGED"

	AssignmentReasonManualClosedInterval = "MANUAL_CLOSED_INTERVAL"
)

// TripRow mirrors the trips table.
type TripRow struct {
	TripID                 string
	VehicleID              string
	StartMs                int64
	EndMs                  int64
	FixCount               int64
	MinLatitude            float64
	MaxLatitude            float64
	MinLongitude           float64
	MaxLongitude           float64
	Status                 string
	CloseReason            string
	SupersededByTripID     *string
	CreatedRevision        int64
	UpdatedRevision        int64
	ClosedAtMs             *int64
	ForcedClose            bool
	MissingFixCountAtClose *int64
}

// TripMergeRow mirrors the trip_merges table.
type TripMergeRow struct {
	PredecessorTripID string
	SuccessorTripID   string
	VehicleID         string
	Revision          int64
	MergedAtMs        int64
}

// ManualBarrierRow mirrors the manual_barriers table.
type ManualBarrierRow struct {
	TripID         string
	VehicleID      string
	StartMs        int64
	EndMs          int64
	ClosedRevision int64
}

// PacketRow mirrors the packets table.
type PacketRow struct {
	SimulatorID      string
	PacketID         string
	VehicleID        string
	CanonicalSHA256  string
	ReceivedAtMs     int64
	AcceptedRevision int64
	StoredResultJSON string
}
