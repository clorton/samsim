package inspectvalidate

import (
	"testing"

	"gpsdemo/internal/inspectclient"
)

// TestI13_AuditOverlapValid verifies that a MERGED predecessor's geometry
// legitimately overlapping its successor's geometry (by design) does NOT
// fail any check -- this is the baseline fx() fixture's trip-pred/trip-succ
// relationship, confirmed passing V07 already by TestI11, reasserted here
// under the I13 label for the specific "audit overlap valid" acceptance
// point.
func TestI13_AuditOverlapValid(t *testing.T) {
	obs := fx()
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V07").Status != StatusPass {
		t.Fatalf("expected V07 PASS with legitimate MERGED audit overlap, got %+v", checkByID(report.Checks, "V07"))
	}
}

// TestI13_LineageCycle_Fails constructs a 2-trip mutual-successor cycle and
// verifies V07 detects it.
func TestI13_LineageCycle_Fails(t *testing.T) {
	obs := fx()
	// Make trip-pred (MERGED into trip-succ) also be trip-succ's
	// predecessor in a loop: give trip-succ an ImmediateSuccessorTripID
	// pointing back at trip-pred, forming trip-pred -> trip-succ -> trip-pred.
	predID := "trip-pred"
	i := findTripIndex(obs, "trip-succ")
	obs.Trips[i].ImmediateSuccessorTripID = &predID
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V07").Status != StatusFail {
		t.Fatalf("expected V07 FAIL on lineage cycle, got %+v", checkByID(report.Checks, "V07"))
	}
}

// TestI13_LineageMissingSuccessorTrip_Fails verifies that a MERGED trip
// pointing at a successor trip ID that was never observed in the
// collection fails V07.
func TestI13_LineageMissingSuccessorTrip_Fails(t *testing.T) {
	obs := fx()
	ghost := "trip-does-not-exist"
	i := findTripIndex(obs, "trip-pred")
	obs.Trips[i].ImmediateSuccessorTripID = &ghost
	obs.Trips[i].TerminalSuccessorTripID = &ghost
	obs.Trips[i].SupersededByTripID = &ghost
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V07").Status != StatusFail {
		t.Fatalf("expected V07 FAIL when successor trip was never observed, got %+v", checkByID(report.Checks, "V07"))
	}
}

// TestI13_LineageWrongSubset_Fails verifies a predecessor geometry fix
// missing entirely from the successor's geometry fails V07 (not just a
// measurement mismatch, as covered by I11, but outright absence).
func TestI13_LineageWrongSubset_Fails(t *testing.T) {
	obs := fx()
	obs.Geometry["trip-succ"] = obs.Geometry["trip-succ"][1:] // drop the shared fix entirely
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V07").Status != StatusFail {
		t.Fatalf("expected V07 FAIL when predecessor fix is entirely absent from successor geometry, got %+v", checkByID(report.Checks, "V07"))
	}
}

// TestI13_DuplicateCurrentMembership_Fails verifies V02 fails when the same
// fix timestamp appears in two different non-MERGED trips' geometry
// (duplicate current membership), independent of the double-membership
// mechanism already covered by I11's TestI11_Corruption_DoubleMembership --
// here constructed via two otherwise-independent mutable trips rather than
// appending into an existing one.
func TestI13_DuplicateCurrentMembership_Fails(t *testing.T) {
	g1 := []inspectclient.FixDTO{fixAt(1000)}
	g2 := []inspectclient.FixDTO{fixAt(1000)} // same timestamp as g1, claimed by a second trip
	obs := twoTripObs(g1, g2)
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V02").Status != StatusFail {
		t.Fatalf("expected V02 FAIL on duplicate current membership across two trips, got %+v", checkByID(report.Checks, "V02"))
	}
}
