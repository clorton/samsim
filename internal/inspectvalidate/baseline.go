package inspectvalidate

import "gpsdemo/internal/inspectclient"

// BaselineSchemaVersion is the current baseline file format version.
const BaselineSchemaVersion = 1

// Baseline is the diagnostic snapshot written/read by --write-baseline and
// --baseline (INS-05). It is normalized domain state only: no read/server
// wall-clock timestamps or other volatile diagnostics, per spec ("V10
// compares normalized captured domain state, excluding read/server wall
// timestamps or other volatile diagnostics").
type Baseline struct {
	SchemaVersion int    `json:"schema_version"`
	DatabaseID    string `json:"database_id"`
	VehicleID     string `json:"vehicle_id"`
	Revision      int64  `json:"revision"`

	TripGapSeconds        int64 `json:"trip_gap_seconds"`
	ExpectedFixIntervalMs int64 `json:"expected_fix_interval_ms"`

	LatestFix *inspectclient.FixDTO `json:"latest_fix"`

	Trips []BaselineTrip `json:"trips"`
}

// BaselineTrip is one trip's normalized metadata plus full ordered fix
// values, sufficient to detect any change to a frozen CLOSED trip later.
type BaselineTrip struct {
	TripID             string               `json:"trip_id"`
	VehicleID          string               `json:"vehicle_id"`
	StartMs            int64                `json:"start_ms"`
	EndMs              int64                `json:"end_ms"`
	FixCount           int64                `json:"fix_count"`
	Bounds             inspectclient.Bounds `json:"bounds"`
	Status             string               `json:"status"`
	CloseReason        string               `json:"close_reason"`
	SupersededByTripID *string              `json:"superseded_by_trip_id"`
	CreatedRevision    int64                `json:"created_revision"`
	UpdatedRevision    int64                `json:"updated_revision"`
	ClosedAtMs         *int64               `json:"closed_at_ms"`
	ForcedClose        bool                 `json:"forced_close"`

	ImmediatePredecessorTripIDs []string `json:"immediate_predecessor_trip_ids"`
	// TerminalSuccessorTripID is captured for reference but deliberately
	// excluded from the frozen-metadata comparison in V09 (spec: "Resolved
	// terminal successor may legitimately change as later successors merge;
	// exclude that derived field from frozen hash and validate it against
	// current graph instead").
	TerminalSuccessorTripID *string `json:"terminal_successor_trip_id"`

	Geometry []inspectclient.FixDTO `json:"geometry"`
}

// ToBaseline converts a coherent Observation into its normalized Baseline
// form.
func ToBaseline(obs *Observation) *Baseline {
	bl := &Baseline{
		SchemaVersion:         BaselineSchemaVersion,
		DatabaseID:            obs.DatabaseID,
		VehicleID:             obs.VehicleID,
		Revision:              obs.Revision,
		TripGapSeconds:        obs.TripGapSeconds,
		ExpectedFixIntervalMs: obs.ExpectedFixIntervalMs,
		LatestFix:             obs.LatestFix,
	}
	for _, t := range obs.Trips {
		bl.Trips = append(bl.Trips, BaselineTrip{
			TripID: t.TripID, VehicleID: t.VehicleID, StartMs: t.StartMs, EndMs: t.EndMs,
			FixCount: t.FixCount, Bounds: t.Bounds, Status: t.Status, CloseReason: t.CloseReason,
			SupersededByTripID: t.SupersededByTripID, CreatedRevision: t.CreatedRevision,
			UpdatedRevision: t.UpdatedRevision, ClosedAtMs: t.ClosedAtMs, ForcedClose: t.ForcedClose,
			ImmediatePredecessorTripIDs: t.ImmediatePredecessorTripIDs,
			TerminalSuccessorTripID:     t.TerminalSuccessorTripID,
			Geometry:                    obs.Geometry[t.TripID],
		})
	}
	return bl
}
