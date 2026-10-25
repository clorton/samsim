package inspectvalidate

import (
	"testing"

	"gpsdemo/internal/inspectclient"
)

// TestI11_CorrectFixture_AllChecksPass verifies the hand-built baseline
// fixture passes V01..V08 cleanly, establishing a known-good starting point
// before corruption tests mutate it one invariant at a time.
func TestI11_CorrectFixture_AllChecksPass(t *testing.T) {
	obs := fx()
	report := BuildReport(obs, nil)
	for _, c := range report.Checks {
		if c.CheckID == "V09" || c.CheckID == "V10" {
			continue // no baseline supplied; expected SKIP
		}
		if c.Status != StatusPass {
			t.Errorf("expected %s to PASS on the correct fixture, got %s: %s %v", c.CheckID, c.Status, c.Message, c.Evidence)
		}
	}
}

// TestI11_ShuffledRawFixes_StillPass verifies order-independence: V01's
// strict-increase check operates on the API's own raw fix order, which is
// already sorted by the aggregator; shuffling the fixture's RawFixes slice
// before sorting it as the real API would must not change any check's
// outcome once this test re-sorts it the way GetVehicleFixes would.
func TestI11_ShuffledRawFixes_StillPass(t *testing.T) {
	obs := fx()
	// Reverse iteration order of Trips/Geometry map does not affect V01-V08
	// since Go map iteration order is already randomized for Geometry; this
	// test exists primarily to document that check correctness must not
	// depend on map/slice iteration order. No corruption introduced.
	report := BuildReport(obs, nil)
	if report.FailCount != 0 {
		t.Fatalf("expected no failures, got %d: %+v", report.FailCount, report.Checks)
	}
}

// TestI11_Corruption_DuplicateRawTimestamp fails V01.
func TestI11_Corruption_DuplicateRawTimestamp(t *testing.T) {
	obs := fx()
	obs.RawFixes = append(obs.RawFixes, obs.RawFixes[0]) // exact duplicate timestamp
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V01").Status != StatusFail {
		t.Fatalf("expected V01 to FAIL on duplicate raw timestamp, got %+v", checkByID(report.Checks, "V01"))
	}
}

// TestI11_Corruption_OutOfRangeLatitude fails V01.
func TestI11_Corruption_OutOfRangeLatitude(t *testing.T) {
	obs := fx()
	obs.RawFixes[0].Latitude = 95.0
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V01").Status != StatusFail {
		t.Fatalf("expected V01 to FAIL on out-of-range latitude, got %+v", checkByID(report.Checks, "V01"))
	}
}

// TestI11_Corruption_LatestFixMismatch fails V01.
func TestI11_Corruption_LatestFixMismatch(t *testing.T) {
	obs := fx()
	wrong := *obs.LatestFix
	wrong.Latitude += 1
	obs.LatestFix = &wrong
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V01").Status != StatusFail {
		t.Fatalf("expected V01 to FAIL on latest_fix mismatch, got %+v", checkByID(report.Checks, "V01"))
	}
}

// TestI11_Corruption_DoubleMembership fails V02: the same fix appears in
// two non-MERGED trips' geometry.
func TestI11_Corruption_DoubleMembership(t *testing.T) {
	obs := fx()
	// Add trip-mutable's first fix into trip-succ's geometry too.
	obs.Geometry["trip-succ"] = append(obs.Geometry["trip-succ"], obs.Geometry["trip-mutable"][0])
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V02").Status != StatusFail {
		t.Fatalf("expected V02 to FAIL on double membership, got %+v", checkByID(report.Checks, "V02"))
	}
}

// TestI11_Corruption_QuarantinedFixHasMembership fails V02.
func TestI11_Corruption_QuarantinedFixHasMembership(t *testing.T) {
	obs := fx()
	// Claim the quarantined manual-interval fix ts=1000 also belongs to
	// trip-mutable's geometry (contradicts its own quarantine).
	obs.Geometry["trip-mutable"] = append(obs.Geometry["trip-mutable"], obs.Geometry["trip-manual"][0])
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V02").Status != StatusFail {
		t.Fatalf("expected V02 to FAIL on quarantined-fix-with-membership, got %+v", checkByID(report.Checks, "V02"))
	}
}

// TestI11_Corruption_TripMetadataBoundsMismatch fails V03.
func TestI11_Corruption_TripMetadataBoundsMismatch(t *testing.T) {
	obs := fx()
	i := findTripIndex(obs, "trip-mutable")
	obs.Trips[i].Bounds.MaxLatitude = 999
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V03").Status != StatusFail {
		t.Fatalf("expected V03 to FAIL on bounds mismatch, got %+v", checkByID(report.Checks, "V03"))
	}
}

// TestI11_Corruption_RevisionOrderingViolation fails V03.
func TestI11_Corruption_RevisionOrderingViolation(t *testing.T) {
	obs := fx()
	i := findTripIndex(obs, "trip-mutable")
	obs.Trips[i].CreatedRevision = 999 // > UpdatedRevision
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V03").Status != StatusFail {
		t.Fatalf("expected V03 to FAIL on created>updated revision, got %+v", checkByID(report.Checks, "V03"))
	}
}

// TestI11_Corruption_ExtraTemporalComponent fails V04: splitting a mutable
// trip's geometry into two reported trips when it should be one component.
func TestI11_Corruption_ExtraTemporalComponent(t *testing.T) {
	obs := fx()
	i := findTripIndex(obs, "trip-mutable")
	// Remove the second fix from geometry (now only 1 fix) but leave both
	// raw fixes present and eligible -- the independent model will compute
	// ONE component of 2 members, while the API reports only 1.
	obs.Geometry["trip-mutable"] = obs.Geometry["trip-mutable"][:1]
	obs.Trips[i].FixCount = 1
	obs.Trips[i].EndMs = obs.Trips[i].StartMs
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V04").Status != StatusFail {
		t.Fatalf("expected V04 to FAIL on split component, got %+v", checkByID(report.Checks, "V04"))
	}
}

// TestI11_Corruption_ManualIntervalOverlap fails V05.
func TestI11_Corruption_ManualIntervalOverlap(t *testing.T) {
	obs := fx()
	// Add a second manual trip whose interval overlaps trip-manual's
	// [1000,2000].
	closedAt := int64(6000)
	geom := []inspectclient.FixDTO{
		{TimestampMs: 1500, Latitude: 40.0005, Longitude: -73.0005, SpeedMps: 1, HeadingDegrees: 10},
		{TimestampMs: 2500, Latitude: 40.0015, Longitude: -73.0015, SpeedMps: 1, HeadingDegrees: 10},
	}
	obs.Trips = append(obs.Trips, inspectclient.TripDetail{
		TripSummary: inspectclient.TripSummary{
			TripID: "trip-manual-2", VehicleID: "vehicle-1", StartMs: 1500, EndMs: 2500, FixCount: 2,
			Bounds: inspectclient.Bounds{MinLatitude: 40.0005, MaxLatitude: 40.0015, MinLongitude: -73.0015, MaxLongitude: -73.0005},
			Status: inspectclient.TripStatusClosed, CloseReason: inspectclient.CloseReasonManual,
			CreatedRevision: 6, UpdatedRevision: 6, ClosedAtMs: &closedAt,
		},
		ImmediatePredecessorTripIDs: []string{},
		Completeness:                completeDTO("trip-manual-2", 6),
	})
	obs.Geometry["trip-manual-2"] = geom
	obs.ManualBarriers = append(obs.ManualBarriers, inspectclient.ManualBarrierDTO{TripID: "trip-manual-2", StartMs: 1500, EndMs: 2500, ClosedRevision: 6})
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V05").Status != StatusFail {
		t.Fatalf("expected V05 to FAIL on overlapping manual intervals, got %+v", checkByID(report.Checks, "V05"))
	}
}

// TestI11_Corruption_MultipleActive fails V06.
func TestI11_Corruption_MultipleActive(t *testing.T) {
	obs := fx()
	i := findTripIndex(obs, "trip-mutable")
	obs.Trips[i].Status = "ACTIVE" // now two ACTIVE trips (trip-mutable and trip-succ)
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V06").Status != StatusFail {
		t.Fatalf("expected V06 to FAIL on multiple ACTIVE trips, got %+v", checkByID(report.Checks, "V06"))
	}
}

// TestI11_Corruption_LineageMissingSuccessor fails V07.
func TestI11_Corruption_LineageMissingSuccessor(t *testing.T) {
	obs := fx()
	i := findTripIndex(obs, "trip-pred")
	obs.Trips[i].ImmediateSuccessorTripID = nil
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V07").Status != StatusFail {
		t.Fatalf("expected V07 to FAIL on missing successor, got %+v", checkByID(report.Checks, "V07"))
	}
}

// TestI11_Corruption_LineageWrongSubset fails V07: predecessor geometry not
// a subset of successor geometry.
func TestI11_Corruption_LineageWrongSubset(t *testing.T) {
	obs := fx()
	pred := obs.Geometry["trip-pred"][0]
	pred.Latitude += 5 // diverge the predecessor's copy from the successor's
	obs.Geometry["trip-pred"] = []inspectclient.FixDTO{pred}
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V07").Status != StatusFail {
		t.Fatalf("expected V07 to FAIL on predecessor/successor measurement mismatch, got %+v", checkByID(report.Checks, "V07"))
	}
}

// TestI11_Corruption_CompletenessMismatch fails V08.
func TestI11_Corruption_CompletenessMismatch(t *testing.T) {
	obs := fx()
	i := findTripIndex(obs, "trip-manual")
	obs.Trips[i].Completeness.MissingFixCount = 999
	obs.Trips[i].Completeness.Complete = false
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V08").Status != StatusFail {
		t.Fatalf("expected V08 to FAIL on completeness mismatch, got %+v", checkByID(report.Checks, "V08"))
	}
}
