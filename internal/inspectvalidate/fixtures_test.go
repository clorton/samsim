package inspectvalidate

import "gpsdemo/internal/inspectclient"

// fx builds a minimal, hand-written, internally-consistent *Observation for
// validate's check suite tests (INS-05's I11). It deliberately does not use
// any aggregator reconstruction code -- every field here is filled in by
// hand to the value the checks are expected to independently verify.
//
// Layout (gap threshold 600s = 600000ms, expected interval 1000ms):
//   - trip-manual: CLOSED/MANUAL, fixes at 1000,2000. Manual members count
//     as "current" membership (V02 includes CLOSED-MANUAL), not quarantine;
//     they are also excluded from independent component computation (V04
//     excludes manual members), and manual intervals are checked by V05.
//   - trip-mutable: INACTIVE, fixes at 700000,701000. Far enough past
//     trip-manual's end (gap 698000ms > 600000ms, though manual members are
//     excluded from gap computation anyway) to stand as its own component.
//   - trip-pred / trip-succ: trip-pred CLOSED/MERGED into trip-succ; both
//     share fix ts=1400000, trip-succ adds ts=1401000. Gap from
//     trip-mutable's end (1400000-701000=699000ms) exceeds the 600000ms
//     threshold, so this is a separate component from trip-mutable's.
func fx() *Observation {
	manualGeom := []inspectclient.FixDTO{
		{TimestampMs: 1000, Latitude: 40.0, Longitude: -73.0, SpeedMps: 1, HeadingDegrees: 10},
		{TimestampMs: 2000, Latitude: 40.001, Longitude: -73.001, SpeedMps: 1, HeadingDegrees: 10},
	}
	mutableGeom := []inspectclient.FixDTO{
		{TimestampMs: 700000, Latitude: 41.0, Longitude: -74.0, SpeedMps: 2, HeadingDegrees: 20},
		{TimestampMs: 701000, Latitude: 41.001, Longitude: -74.001, SpeedMps: 2, HeadingDegrees: 20},
	}
	predGeom := []inspectclient.FixDTO{
		{TimestampMs: 1400000, Latitude: 42.0, Longitude: -75.0, SpeedMps: 3, HeadingDegrees: 30},
	}
	succGeom := []inspectclient.FixDTO{
		predGeom[0],
		{TimestampMs: 1401000, Latitude: 42.001, Longitude: -75.001, SpeedMps: 3, HeadingDegrees: 30},
	}

	var rawFixes []inspectclient.FixDTO
	rawFixes = append(rawFixes, manualGeom...)
	rawFixes = append(rawFixes, mutableGeom...)
	rawFixes = append(rawFixes, succGeom...) // succGeom's two fixes cover both pred/succ members

	// current_trip_id annotations on the raw copies. Manual members count
	// as current membership of their own CLOSED/MANUAL trip (V02); none of
	// these fixes are quarantined in the correct fixture.
	for i := range rawFixes {
		switch rawFixes[i].TimestampMs {
		case 1000, 2000:
			id := "trip-manual"
			rawFixes[i].CurrentTripID = &id
		case 700000, 701000:
			id := "trip-mutable"
			rawFixes[i].CurrentTripID = &id
		case 1400000, 1401000:
			id := "trip-succ"
			rawFixes[i].CurrentTripID = &id
		}
	}

	manualClosedAt := int64(5000)
	succ := "trip-succ"

	tripManual := inspectclient.TripDetail{
		TripSummary: inspectclient.TripSummary{
			TripID: "trip-manual", VehicleID: "vehicle-1", StartMs: 1000, EndMs: 2000, FixCount: 2,
			Bounds: inspectclient.Bounds{MinLatitude: 40.0, MaxLatitude: 40.001, MinLongitude: -73.001, MaxLongitude: -73.0},
			Status: inspectclient.TripStatusClosed, CloseReason: inspectclient.CloseReasonManual,
			CreatedRevision: 1, UpdatedRevision: 2, ClosedAtMs: &manualClosedAt, ForcedClose: false,
		},
		ImmediatePredecessorTripIDs: []string{},
		Completeness:                completeDTO("trip-manual", 2),
	}
	tripMutable := inspectclient.TripDetail{
		TripSummary: inspectclient.TripSummary{
			TripID: "trip-mutable", VehicleID: "vehicle-1", StartMs: 700000, EndMs: 701000, FixCount: 2,
			Bounds: inspectclient.Bounds{MinLatitude: 41.0, MaxLatitude: 41.001, MinLongitude: -74.001, MaxLongitude: -74.0},
			Status: inspectclient.TripStatusInactive, CloseReason: inspectclient.CloseReasonNone,
			CreatedRevision: 3, UpdatedRevision: 3,
		},
		ImmediatePredecessorTripIDs: []string{},
		Completeness:                completeDTO("trip-mutable", 3),
	}
	tripPred := inspectclient.TripDetail{
		TripSummary: inspectclient.TripSummary{
			TripID: "trip-pred", VehicleID: "vehicle-1", StartMs: 1400000, EndMs: 1400000, FixCount: 1,
			Bounds: inspectclient.Bounds{MinLatitude: 42.0, MaxLatitude: 42.0, MinLongitude: -75.0, MaxLongitude: -75.0},
			Status: inspectclient.TripStatusClosed, CloseReason: inspectclient.CloseReasonMerged,
			SupersededByTripID: &succ,
			CreatedRevision:    4, UpdatedRevision: 5,
		},
		ImmediatePredecessorTripIDs: []string{},
		ImmediateSuccessorTripID:    &succ,
		TerminalSuccessorTripID:     &succ,
		Completeness:                completeDTO("trip-pred", 5),
	}
	tripSucc := inspectclient.TripDetail{
		TripSummary: inspectclient.TripSummary{
			TripID: "trip-succ", VehicleID: "vehicle-1", StartMs: 1400000, EndMs: 1401000, FixCount: 2,
			Bounds: inspectclient.Bounds{MinLatitude: 42.0, MaxLatitude: 42.001, MinLongitude: -75.001, MaxLongitude: -75.0},
			Status: inspectclient.TripStatusActive, CloseReason: inspectclient.CloseReasonNone,
			CreatedRevision: 5, UpdatedRevision: 5,
		},
		ImmediatePredecessorTripIDs: []string{"trip-pred"},
		Completeness:                completeDTO("trip-succ", 5),
	}

	return &Observation{
		DatabaseID: "db-1", VehicleID: "vehicle-1", Revision: 5,
		TripGapSeconds: 600, ExpectedFixIntervalMs: 1000,
		LatestFix: &succGeom[1],
		ManualBarriers: []inspectclient.ManualBarrierDTO{
			{TripID: "trip-manual", StartMs: 1000, EndMs: 2000, ClosedRevision: 2},
		},
		Trips: []inspectclient.TripDetail{tripManual, tripMutable, tripPred, tripSucc},
		Geometry: map[string][]inspectclient.FixDTO{
			"trip-manual":  manualGeom,
			"trip-mutable": mutableGeom,
			"trip-pred":    predGeom,
			"trip-succ":    succGeom,
		},
		RawFixes: rawFixes,
	}
}

func completeDTO(tripID string, revision int64) *inspectclient.CompletenessDTO {
	return &inspectclient.CompletenessDTO{
		TripID: tripID, Revision: revision, ExpectedFixIntervalMs: 1000,
		MissingFixCount: 0, Complete: true, BoundaryCompleteness: "UNKNOWN", Assessment: "NO_INTERNAL_GAPS",
	}
}

// findTripIndex returns the index of the named trip within obs.Trips, for
// mutation in tests.
func findTripIndex(obs *Observation, tripID string) int {
	for i, t := range obs.Trips {
		if t.TripID == tripID {
			return i
		}
	}
	return -1
}

func checkByID(checks []Check, id string) Check {
	for _, c := range checks {
		if c.CheckID == id {
			return c
		}
	}
	return Check{}
}
