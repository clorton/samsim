package aggregator

import "gpsdemo/internal/storage"

// Bounds mirrors AG-07's trip summary bounds object.
type Bounds struct {
	MinLatitude  float64 `json:"min_latitude"`
	MaxLatitude  float64 `json:"max_latitude"`
	MinLongitude float64 `json:"min_longitude"`
	MaxLongitude float64 `json:"max_longitude"`
}

// TripSummary mirrors AG-07's trip summary DTO.
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

func tripSummaryFromRow(t storage.TripRow) TripSummary {
	return TripSummary{
		TripID:    t.TripID,
		VehicleID: t.VehicleID,
		StartMs:   t.StartMs,
		EndMs:     t.EndMs,
		FixCount:  t.FixCount,
		Bounds: Bounds{
			MinLatitude:  t.MinLatitude,
			MaxLatitude:  t.MaxLatitude,
			MinLongitude: t.MinLongitude,
			MaxLongitude: t.MaxLongitude,
		},
		Status:             t.Status,
		CloseReason:        t.CloseReason,
		SupersededByTripID: t.SupersededByTripID,
		CreatedRevision:    t.CreatedRevision,
		UpdatedRevision:    t.UpdatedRevision,
		ClosedAtMs:         t.ClosedAtMs,
		ForcedClose:        t.ForcedClose,
	}
}

// TripDetail adds lineage and closure assessment to TripSummary (AG-07).
type TripDetail struct {
	TripSummary
	ImmediatePredecessorTripIDs []string         `json:"immediate_predecessor_trip_ids"`
	ImmediateSuccessorTripID    *string          `json:"immediate_successor_trip_id"`
	TerminalSuccessorTripID     *string          `json:"terminal_successor_trip_id"`
	Completeness                *CompletenessDTO `json:"completeness,omitempty"`
}

// FixDTO mirrors AG-07's raw fix DTO, used for vehicle/trip fix listings.
type FixDTO struct {
	TimestampMs      int64   `json:"timestamp_ms"`
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
	SpeedMps         float64 `json:"speed_mps"`
	HeadingDegrees   float64 `json:"heading_degrees"`
	CurrentTripID    *string `json:"current_trip_id"`
	AssignmentReason *string `json:"assignment_reason"`
}

func fixDTOFromRow(f storage.FixRow, currentTripID *string) FixDTO {
	return FixDTO{
		TimestampMs:      f.TimestampMs,
		Latitude:         f.Latitude,
		Longitude:        f.Longitude,
		SpeedMps:         f.SpeedMps,
		HeadingDegrees:   f.HeadingDegrees,
		CurrentTripID:    currentTripID,
		AssignmentReason: f.AssignmentReason,
	}
}

// VehicleDTO mirrors AG-07's vehicle detail DTO.
type VehicleDTO struct {
	VehicleID             string  `json:"vehicle_id"`
	LatestFix             *FixDTO `json:"latest_fix"`
	ActiveTripID          *string `json:"active_trip_id"`
	Revision              int64   `json:"revision"`
	DatabaseID            string  `json:"database_id"`
	TripGapSeconds        int64   `json:"trip_gap_seconds"`
	ExpectedFixIntervalMs int64   `json:"expected_fix_interval_ms"`
}

// VehicleListItem mirrors AG-07's global vehicle list item (no latest fix
// detail required there; kept intentionally light to match the spec's "all
// known vehicles" listing semantics, which only needs ID + activity +
// per-item revision).
type VehicleListItem struct {
	VehicleID    string  `json:"vehicle_id"`
	ActiveTripID *string `json:"active_trip_id"`
	Revision     int64   `json:"revision"`
}

// ManualBarrierDTO mirrors one immutable manual closure interval.
type ManualBarrierDTO struct {
	TripID         string `json:"trip_id"`
	StartMs        int64  `json:"start_ms"`
	EndMs          int64  `json:"end_ms"`
	ClosedRevision int64  `json:"closed_revision"`
}

// SnapshotDTO mirrors AG-07's coherent vehicle snapshot.
type SnapshotDTO struct {
	Vehicle        VehicleDTO         `json:"vehicle"`
	Trips          []TripSummary      `json:"trips"`
	ManualBarriers []ManualBarrierDTO `json:"manual_barriers"`
	Revision       int64              `json:"revision"`
	DatabaseID     string             `json:"database_id"`
}

// ListEnvelope is the generic pagination envelope for per-vehicle lists
// (AG-07). For the global vehicle list, Revision is left zero and omitted
// by the caller (no invented global revision).
type ListEnvelope[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
	Revision   int64   `json:"revision,omitempty"`
	DatabaseID string  `json:"database_id"`
}
