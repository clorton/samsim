package inspectclient

// This file declares gpsinspect's own REST DTOs, decoded with explicit
// integer/string types (never through an intermediate map[string]any, which
// would silently widen int64 timestamps/revisions to float64). Field names
// and shapes mirror AG-07's published JSON contract exactly (see
// aggregator.md and internal/aggregator/dto*.go), but these types are
// declared independently rather than imported from internal/aggregator: the
// inspector is a pure HTTP client, with no dependency on the aggregator's
// storage/service internals, matching the simulator's established
// independence from aggregator-internal packages in this codebase. Unknown
// JSON fields are tolerated (ordinary encoding/json.Unmarshal behavior) for
// forward compatibility, per INS-01.

// Bounds mirrors a trip summary's coordinate extrema.
type Bounds struct {
	MinLatitude  float64 `json:"min_latitude"`
	MaxLatitude  float64 `json:"max_latitude"`
	MinLongitude float64 `json:"min_longitude"`
	MaxLongitude float64 `json:"max_longitude"`
}

// TripSummary mirrors one trip list/snapshot entry.
type TripSummary struct {
	TripID             string  `json:"trip_id"`
	VehicleID          string  `json:"vehicle_id"`
	StartMs            int64   `json:"start_ms"`
	EndMs              int64   `json:"end_ms"`
	FixCount           int64   `json:"fix_count"`
	Bounds             Bounds  `json:"bounds"`
	Status             string  `json:"status"`
	CloseReason        string  `json:"close_reason"`
	SupersededByTripID *string `json:"superseded_by_trip_id"`
	CreatedRevision    int64   `json:"created_revision"`
	UpdatedRevision    int64   `json:"updated_revision"`
	ClosedAtMs         *int64  `json:"closed_at_ms"`
	ForcedClose        bool    `json:"forced_close"`
}

// TripDetail adds lineage and completeness to TripSummary.
type TripDetail struct {
	TripSummary
	ImmediatePredecessorTripIDs []string         `json:"immediate_predecessor_trip_ids"`
	ImmediateSuccessorTripID    *string          `json:"immediate_successor_trip_id"`
	TerminalSuccessorTripID     *string          `json:"terminal_successor_trip_id"`
	Completeness                *CompletenessDTO `json:"completeness,omitempty"`
}

// FixDTO mirrors one raw/geometry fix entry.
type FixDTO struct {
	TimestampMs      int64   `json:"timestamp_ms"`
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
	SpeedMps         float64 `json:"speed_mps"`
	HeadingDegrees   float64 `json:"heading_degrees"`
	CurrentTripID    *string `json:"current_trip_id"`
	AssignmentReason *string `json:"assignment_reason"`
}

// VehicleDTO mirrors the vehicle detail response.
type VehicleDTO struct {
	VehicleID             string  `json:"vehicle_id"`
	LatestFix             *FixDTO `json:"latest_fix"`
	ActiveTripID          *string `json:"active_trip_id"`
	Revision              int64   `json:"revision"`
	DatabaseID            string  `json:"database_id"`
	TripGapSeconds        int64   `json:"trip_gap_seconds"`
	ExpectedFixIntervalMs int64   `json:"expected_fix_interval_ms"`
}

// VehicleListItem mirrors one entry of the global vehicle list.
type VehicleListItem struct {
	VehicleID    string  `json:"vehicle_id"`
	ActiveTripID *string `json:"active_trip_id"`
	Revision     int64   `json:"revision"`
}

// ManualBarrierDTO mirrors one immutable manual-closure interval.
type ManualBarrierDTO struct {
	TripID         string `json:"trip_id"`
	StartMs        int64  `json:"start_ms"`
	EndMs          int64  `json:"end_ms"`
	ClosedRevision int64  `json:"closed_revision"`
}

// SnapshotDTO mirrors the coherent vehicle snapshot.
type SnapshotDTO struct {
	Vehicle        VehicleDTO         `json:"vehicle"`
	Trips          []TripSummary      `json:"trips"`
	ManualBarriers []ManualBarrierDTO `json:"manual_barriers"`
	Revision       int64              `json:"revision"`
	DatabaseID     string             `json:"database_id"`
}

// MissingIntervalDTO mirrors one reported internal gap.
type MissingIntervalDTO struct {
	StartMs      int64 `json:"start_ms"`
	EndMs        int64 `json:"end_ms"`
	MissingCount int64 `json:"missing_count"`
}

// CompletenessDTO mirrors a trip's completeness assessment.
type CompletenessDTO struct {
	TripID                string               `json:"trip_id"`
	Revision              int64                `json:"revision"`
	ExpectedFixIntervalMs int64                `json:"expected_fix_interval_ms"`
	MissingFixCount       int64                `json:"missing_fix_count"`
	MissingIntervals      []MissingIntervalDTO `json:"missing_intervals"`
	Complete              bool                 `json:"complete"`
	BoundaryCompleteness  string               `json:"boundary_completeness"`
	Assessment            string               `json:"assessment"`
}

// CloseResult mirrors the POST .../close response body.
type CloseResult struct {
	TripID             string           `json:"trip_id"`
	Status             string           `json:"status"`
	CloseReason        string           `json:"close_reason"`
	Revision           int64            `json:"revision"`
	ForcedClose        bool             `json:"forced_close"`
	Completeness       *CompletenessDTO `json:"completeness,omitempty"`
	SupersededByTripID *string          `json:"superseded_by_trip_id,omitempty"`
}

// HealthStatus mirrors GET /api/v1/health.
type HealthStatus struct {
	Healthy        bool   `json:"healthy"`
	DatabaseID     string `json:"database_id"`
	TripGapSeconds int64  `json:"trip_gap_seconds"`
}

// ListEnvelope is the generic pagination envelope.
type ListEnvelope[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
	Revision   int64   `json:"revision,omitempty"`
	DatabaseID string  `json:"database_id"`
}

// Trip status/close-reason/assignment-reason constants, mirrored from
// AG-03's CHECK constraints for use in output formatting and validation.
const (
	TripStatusActive   = "ACTIVE"
	TripStatusInactive = "INACTIVE"
	TripStatusClosed   = "CLOSED"

	CloseReasonNone   = "NONE"
	CloseReasonManual = "MANUAL"
	CloseReasonMerged = "MERGED"

	AssignmentReasonManualClosedInterval = "MANUAL_CLOSED_INTERVAL"
)
