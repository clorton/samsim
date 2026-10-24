package aggregator

import (
	"testing"

	"gpsdemo/internal/storage"
)

// TestMergeChain_RepeatedMergesResolvePredecessorChains implements T07:
// repeated merges resolve predecessor->successor chains; closed predecessor
// membership/bounds remain unchanged; no cycles.
func TestMergeChain_RepeatedMergesResolvePredecessorChains(t *testing.T) {
	svc, store := newTestService(t)

	// Trip A: [0, 600_000] and Trip B: [1_800_000, 2_400_000], both gaps
	// exactly 600s internally, but 1_200_000ms apart from each other
	// (>600s), so initially two separate trips.
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1}, fixSpec{600_000, 1, 1},
		fixSpec{1_800_000, 1, 1}, fixSpec{2_400_000, 1, 1},
	))
	mutable := listMutableTrips(t, store, "vehicle-1")
	if len(mutable) != 2 {
		t.Fatalf("expected 2 initial trips, got %d", len(mutable))
	}
	tripA := mutable[0].TripID // earlier start_ms

	// Bridge A and B at +1_200_000 (600s from each neighbor) -> merge into trip C.
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{1_200_000, 1, 1}))

	mutableAfterFirstMerge := listMutableTrips(t, store, "vehicle-1")
	if len(mutableAfterFirstMerge) != 1 {
		t.Fatalf("expected 1 trip after first merge, got %d", len(mutableAfterFirstMerge))
	}
	tripC := mutableAfterFirstMerge[0].TripID

	aRow := getTrip(t, store, tripA)
	if aRow.CloseReason != storage.CloseReasonMerged {
		t.Fatalf("expected trip A merged, got %+v", aRow)
	}
	aFixCountAfterFirstMerge := aRow.FixCount

	// Trip D: far away (>600s from trip C's end at 2_400_000), separate trip.
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000003", baseMs, fixSpec{3_600_000, 1, 1}))
	mutableBeforeSecondMerge := listMutableTrips(t, store, "vehicle-1")
	if len(mutableBeforeSecondMerge) != 2 {
		t.Fatalf("expected 2 mutable trips, got %d", len(mutableBeforeSecondMerge))
	}

	// Bridge trip C (ends 2_400_000) and trip D (starts 3_600_000) at
	// +3_000_000 (600s from each neighbor) -> merge into trip E. This tests
	// a SECOND merge, where one of the predecessors (tripC) was itself a
	// successor of an earlier merge.
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000004", baseMs, fixSpec{3_000_000, 1, 1}))

	mutableFinal := listMutableTrips(t, store, "vehicle-1")
	if len(mutableFinal) != 1 {
		t.Fatalf("expected 1 final trip, got %d", len(mutableFinal))
	}
	tripE := mutableFinal[0].TripID

	// Trip C must now be CLOSED/MERGED into trip E.
	cRow := getTrip(t, store, tripC)
	if cRow.CloseReason != storage.CloseReasonMerged || cRow.SupersededByTripID == nil || *cRow.SupersededByTripID != tripE {
		t.Fatalf("expected trip C merged into E, got %+v", cRow)
	}

	// Trip A's frozen membership/bounds from the FIRST merge must remain
	// completely unchanged by the SECOND merge.
	aRowAfterSecondMerge := getTrip(t, store, tripA)
	if aRowAfterSecondMerge.FixCount != aFixCountAfterFirstMerge {
		t.Fatalf("expected trip A fix_count frozen at %d, got %d", aFixCountAfterFirstMerge, aRowAfterSecondMerge.FixCount)
	}
	if aRowAfterSecondMerge.SupersededByTripID == nil || *aRowAfterSecondMerge.SupersededByTripID != tripC {
		t.Fatalf("expected trip A's immediate successor to remain trip C, got %v", aRowAfterSecondMerge.SupersededByTripID)
	}

	// Resolve chain A -> C -> E via the query layer.
	detailA, err := svc.GetTripDetail(ctxBg(), tripA, nil)
	if err != nil {
		t.Fatal(err)
	}
	if detailA.ImmediateSuccessorTripID == nil || *detailA.ImmediateSuccessorTripID != tripC {
		t.Fatalf("expected A's immediate successor = C, got %v", detailA.ImmediateSuccessorTripID)
	}
	if detailA.TerminalSuccessorTripID == nil || *detailA.TerminalSuccessorTripID != tripE {
		t.Fatalf("expected A's terminal successor = E, got %v", detailA.TerminalSuccessorTripID)
	}

	detailC, err := svc.GetTripDetail(ctxBg(), tripC, nil)
	if err != nil {
		t.Fatal(err)
	}
	if detailC.ImmediateSuccessorTripID == nil || *detailC.ImmediateSuccessorTripID != tripE {
		t.Fatalf("expected C's immediate successor = E, got %v", detailC.ImmediateSuccessorTripID)
	}
	if detailC.TerminalSuccessorTripID == nil || *detailC.TerminalSuccessorTripID != tripE {
		t.Fatalf("expected C's terminal successor = E, got %v", detailC.TerminalSuccessorTripID)
	}

	// No cycles: E itself must have no successor.
	detailE, err := svc.GetTripDetail(ctxBg(), tripE, nil)
	if err != nil {
		t.Fatal(err)
	}
	if detailE.ImmediateSuccessorTripID != nil {
		t.Fatalf("expected terminal trip E to have no successor, got %v", detailE.ImmediateSuccessorTripID)
	}
}
