package inspectvalidate

import (
	"gpsdemo/internal/inspectclient"
	"testing"
)

// singleTripObs builds a minimal one-trip, one-vehicle observation for
// focused completeness/boundary tests, independent of the larger fx()
// fixture's multi-trip lineage.
func singleTripObs(geom []inspectclient.FixDTO, status, closeReason string, missingFixCount int64, complete bool) *Observation {
	var raw []inspectclient.FixDTO
	for _, f := range geom {
		fc := f
		id := "trip-1"
		fc.CurrentTripID = &id
		raw = append(raw, fc)
	}
	latest := raw[len(raw)-1]
	return &Observation{
		DatabaseID: "db-1", VehicleID: "vehicle-1", Revision: 1,
		TripGapSeconds: 600, ExpectedFixIntervalMs: 1000,
		LatestFix: &latest,
		Trips: []inspectclient.TripDetail{{
			TripSummary: inspectclient.TripSummary{
				TripID: "trip-1", VehicleID: "vehicle-1", StartMs: geom[0].TimestampMs, EndMs: geom[len(geom)-1].TimestampMs,
				FixCount: int64(len(geom)),
				Bounds:   boundsOf(geom),
				Status:   status, CloseReason: closeReason, CreatedRevision: 1, UpdatedRevision: 1,
			},
			ImmediatePredecessorTripIDs: []string{},
			Completeness: &inspectclient.CompletenessDTO{
				TripID: "trip-1", Revision: 1, ExpectedFixIntervalMs: 1000,
				MissingFixCount: missingFixCount, Complete: complete, BoundaryCompleteness: "UNKNOWN",
				Assessment: assessmentFor(complete),
			},
		}},
		Geometry: map[string][]inspectclient.FixDTO{"trip-1": geom},
		RawFixes: raw,
	}
}

func assessmentFor(complete bool) string {
	if complete {
		return "NO_INTERNAL_GAPS"
	}
	return "INTERNAL_GAPS"
}

func boundsOf(geom []inspectclient.FixDTO) inspectclient.Bounds {
	b := inspectclient.Bounds{MinLatitude: geom[0].Latitude, MaxLatitude: geom[0].Latitude, MinLongitude: geom[0].Longitude, MaxLongitude: geom[0].Longitude}
	for _, f := range geom {
		if f.Latitude < b.MinLatitude {
			b.MinLatitude = f.Latitude
		}
		if f.Latitude > b.MaxLatitude {
			b.MaxLatitude = f.Latitude
		}
		if f.Longitude < b.MinLongitude {
			b.MinLongitude = f.Longitude
		}
		if f.Longitude > b.MaxLongitude {
			b.MaxLongitude = f.Longitude
		}
	}
	return b
}

func fixAt(ts int64) inspectclient.FixDTO {
	return inspectclient.FixDTO{TimestampMs: ts, Latitude: 40.0, Longitude: -73.0, SpeedMps: 1, HeadingDegrees: 10}
}

// TestI12_GapBoundary_599999ms is just under the 600s gap threshold: the
// independent model must treat it as ONE component (V04 passes), matching a
// single ACTIVE/INACTIVE trip.
func TestI12_GapBoundary_599999ms(t *testing.T) {
	geom := []inspectclient.FixDTO{fixAt(1000), fixAtMs(1000 + 599999)}
	obs := singleTripObs(geom, inspectclient.TripStatusActive, inspectclient.CloseReasonNone, 0, true)
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V04").Status != StatusPass {
		t.Fatalf("expected V04 PASS at 599999ms gap (just under threshold), got %+v", checkByID(report.Checks, "V04"))
	}
}

// TestI12_GapBoundary_600000ms is exactly at the 600s (inclusive) gap
// threshold: still one component.
func TestI12_GapBoundary_600000ms(t *testing.T) {
	geom := []inspectclient.FixDTO{fixAt(1000), fixAtMs(1000 + 600000)}
	obs := singleTripObs(geom, inspectclient.TripStatusActive, inspectclient.CloseReasonNone, 0, true)
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V04").Status != StatusPass {
		t.Fatalf("expected V04 PASS at exactly 600000ms gap (inclusive threshold), got %+v", checkByID(report.Checks, "V04"))
	}
}

// TestI12_GapBoundary_600001ms is just over the threshold: this must be two
// separate components. We model it as two separate ACTIVE/INACTIVE trips to
// match what the real aggregator would produce, and verify V04 passes for
// THAT correctly-split case (a single merged trip here would instead fail,
// which is exactly V04's job).
func TestI12_GapBoundary_600001ms(t *testing.T) {
	g1 := []inspectclient.FixDTO{fixAt(1000)}
	g2 := []inspectclient.FixDTO{fixAtMs(1000 + 600001)}
	obs := twoTripObs(g1, g2)
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V04").Status != StatusPass {
		t.Fatalf("expected V04 PASS for correctly-split trips at 600001ms gap, got %+v", checkByID(report.Checks, "V04"))
	}

	// Now verify the NEGATIVE case: if these two were incorrectly reported
	// as ONE trip instead, V04 must FAIL.
	merged := singleTripObs(append(append([]inspectclient.FixDTO{}, g1...), g2...), inspectclient.TripStatusActive, inspectclient.CloseReasonNone, 0, false)
	// Completeness aside (V08 is separate), V04 must still fail since one
	// component is reported where two should exist.
	report2 := BuildReport(merged, nil)
	if checkByID(report2.Checks, "V04").Status != StatusFail {
		t.Fatalf("expected V04 FAIL when >600s gap is incorrectly kept as one trip, got %+v", checkByID(report2.Checks, "V04"))
	}
}

func fixAtMs(ts int64) inspectclient.FixDTO {
	return inspectclient.FixDTO{TimestampMs: ts, Latitude: 40.0, Longitude: -73.0, SpeedMps: 1, HeadingDegrees: 10}
}

func twoTripObs(g1, g2 []inspectclient.FixDTO) *Observation {
	var raw []inspectclient.FixDTO
	id1, id2 := "trip-1", "trip-2"
	for _, f := range g1 {
		fc := f
		fc.CurrentTripID = &id1
		raw = append(raw, fc)
	}
	for _, f := range g2 {
		fc := f
		fc.CurrentTripID = &id2
		raw = append(raw, fc)
	}
	latest := raw[len(raw)-1]
	return &Observation{
		DatabaseID: "db-1", VehicleID: "vehicle-1", Revision: 1,
		TripGapSeconds: 600, ExpectedFixIntervalMs: 1000,
		LatestFix: &latest,
		Trips: []inspectclient.TripDetail{
			{
				TripSummary: inspectclient.TripSummary{
					TripID: id1, VehicleID: "vehicle-1", StartMs: g1[0].TimestampMs, EndMs: g1[len(g1)-1].TimestampMs,
					FixCount: int64(len(g1)), Bounds: boundsOf(g1), Status: inspectclient.TripStatusInactive, CloseReason: inspectclient.CloseReasonNone,
					CreatedRevision: 1, UpdatedRevision: 1,
				},
				ImmediatePredecessorTripIDs: []string{},
				Completeness:                completeDTO(id1, 1),
			},
			{
				TripSummary: inspectclient.TripSummary{
					TripID: id2, VehicleID: "vehicle-1", StartMs: g2[0].TimestampMs, EndMs: g2[len(g2)-1].TimestampMs,
					FixCount: int64(len(g2)), Bounds: boundsOf(g2), Status: inspectclient.TripStatusActive, CloseReason: inspectclient.CloseReasonNone,
					CreatedRevision: 2, UpdatedRevision: 2,
				},
				ImmediatePredecessorTripIDs: []string{},
				Completeness:                completeDTO(id2, 2),
			},
		},
		Geometry: map[string][]inspectclient.FixDTO{id1: g1, id2: g2},
		RawFixes: raw,
	}
}

// TestI12_SingletonTrip verifies a trip with exactly one fix is valid (no
// completeness computation possible/needed, V04 treats it as its own
// component).
func TestI12_SingletonTrip(t *testing.T) {
	geom := []inspectclient.FixDTO{fixAt(1000)}
	obs := singleTripObs(geom, inspectclient.TripStatusActive, inspectclient.CloseReasonNone, 0, true)
	report := BuildReport(obs, nil)
	if report.FailCount != 0 {
		t.Fatalf("expected a valid singleton trip to pass all checks, got failures: %+v", report.Checks)
	}
}

// TestI12_InternalCompletenessGap verifies V08 independently reproduces a
// known missing-fix count/interval for an internal gap.
func TestI12_InternalCompletenessGap(t *testing.T) {
	// 1000 -> 12000 is an 11000ms gap at 1000ms interval -> 10 missing in one
	// interval.
	geom := []inspectclient.FixDTO{fixAt(1000), fixAtMs(12000)}
	obs := singleTripObs(geom, inspectclient.TripStatusActive, inspectclient.CloseReasonNone, 10, false)
	obs.Trips[0].Completeness.MissingIntervals = []inspectclient.MissingIntervalDTO{
		{StartMs: 2000, EndMs: 11000, MissingCount: 10},
	}
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V08").Status != StatusPass {
		t.Fatalf("expected V08 PASS (matching independently computed 10 missing), got %+v", checkByID(report.Checks, "V08"))
	}
}

// TestI12_NoActivityTimeout verifies a trip is still a valid ACTIVE trip
// with no special timeout logic: silence alone does not close/deactivate.
func TestI12_NoActivityTimeout(t *testing.T) {
	geom := []inspectclient.FixDTO{fixAt(1000), fixAtMs(2000)}
	obs := singleTripObs(geom, inspectclient.TripStatusActive, inspectclient.CloseReasonNone, 0, true)
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V06").Status != StatusPass {
		t.Fatalf("expected V06 PASS (no activity timeout concept), got %+v", checkByID(report.Checks, "V06"))
	}
}

// TestI12_LatestQuarantinedFix_NoActiveTrip verifies V06's rule: if the
// single latest raw fix is quarantined (inside a manual interval), no trip
// should be ACTIVE, even though an older mutable trip exists.
func TestI12_LatestQuarantinedFix_NoActiveTrip(t *testing.T) {
	mutableGeom := []inspectclient.FixDTO{fixAt(1000), fixAtMs(2000)}
	manualGeom := []inspectclient.FixDTO{fixAtMs(700000), fixAtMs(701000)}

	var raw []inspectclient.FixDTO
	mutID := "trip-mutable"
	for _, f := range mutableGeom {
		fc := f
		fc.CurrentTripID = &mutID
		raw = append(raw, fc)
	}
	manualReason := inspectclient.AssignmentReasonManualClosedInterval
	manID := "trip-manual"
	for i, f := range manualGeom {
		fc := f
		if i == len(manualGeom)-1 {
			// The LATEST raw fix overall is inside the manual interval.
			fc.AssignmentReason = nil
			fc.CurrentTripID = &manID
		} else {
			fc.CurrentTripID = &manID
		}
		raw = append(raw, fc)
	}
	_ = manualReason
	latest := raw[len(raw)-1]
	closedAt := int64(800000)

	obs := &Observation{
		DatabaseID: "db-1", VehicleID: "vehicle-1", Revision: 2,
		TripGapSeconds: 600, ExpectedFixIntervalMs: 1000,
		LatestFix: &latest,
		ManualBarriers: []inspectclient.ManualBarrierDTO{
			{TripID: "trip-manual", StartMs: 700000, EndMs: 701000, ClosedRevision: 2},
		},
		Trips: []inspectclient.TripDetail{
			{
				TripSummary: inspectclient.TripSummary{
					TripID: mutID, VehicleID: "vehicle-1", StartMs: 1000, EndMs: 2000, FixCount: 2,
					Bounds: boundsOf(mutableGeom), Status: inspectclient.TripStatusInactive, CloseReason: inspectclient.CloseReasonNone,
					CreatedRevision: 1, UpdatedRevision: 1,
				},
				ImmediatePredecessorTripIDs: []string{},
				Completeness:                completeDTO(mutID, 1),
			},
			{
				TripSummary: inspectclient.TripSummary{
					TripID: manID, VehicleID: "vehicle-1", StartMs: 700000, EndMs: 701000, FixCount: 2,
					Bounds: boundsOf(manualGeom), Status: inspectclient.TripStatusClosed, CloseReason: inspectclient.CloseReasonManual,
					CreatedRevision: 2, UpdatedRevision: 2, ClosedAtMs: &closedAt,
				},
				ImmediatePredecessorTripIDs: []string{},
				Completeness:                completeDTO(manID, 2),
			},
		},
		Geometry: map[string][]inspectclient.FixDTO{mutID: mutableGeom, manID: manualGeom},
		RawFixes: raw,
	}

	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V06").Status != StatusPass {
		t.Fatalf("expected V06 PASS (no ACTIVE trip; latest fix is manual), got %+v", checkByID(report.Checks, "V06"))
	}
}
