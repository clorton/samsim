package aggregator

import (
	"context"
	"errors"
	"testing"

	"gpsdemo/internal/apierr"
	"gpsdemo/internal/storage"
)

func ctxBg() context.Context { return context.Background() }

// TestClose_PrecommitFailureLeavesNoTrace extends T14's crash-injection
// coverage to AG-06 closure: a failure staged after Close has prepared all
// writes but before commit must roll back entirely, leaving the trip
// mutable and the manual barrier absent.
func TestClose_PrecommitFailureLeavesNoTrace(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1})
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID

	injected := errors.New("simulated precommit failure")
	store.SetPrecommitFailureHook(func() error { return injected })
	_, err := svc.Close(ctxBg(), tripID, res.Revision, false)
	store.SetPrecommitFailureHook(nil)

	if err == nil || !errors.Is(err, injected) {
		t.Fatalf("expected injected precommit failure to propagate, got %v", err)
	}

	// Trip must remain mutable (not CLOSED), and no barrier was inserted.
	trip := getTrip(t, store, tripID)
	if trip.Status == storage.TripStatusClosed {
		t.Fatalf("expected trip to remain unclosed after rolled-back close, got %+v", trip)
	}
	barriers := listManualBarriers(t, store, "vehicle-1")
	if len(barriers) != 0 {
		t.Fatalf("expected no manual barrier after rolled-back close, got %+v", barriers)
	}

	// Vehicle revision must be unchanged.
	vehicle := getVehicle(t, store, "vehicle-1")
	if vehicle.Revision != res.Revision {
		t.Fatalf("expected revision unchanged at %d, got %d", res.Revision, vehicle.Revision)
	}

	// A normal retry of the close must now succeed cleanly.
	closeRes, err := svc.Close(ctxBg(), tripID, res.Revision, false)
	if err != nil {
		t.Fatalf("expected retry close to succeed after rollback, got %v", err)
	}
	if closeRes.Status != storage.TripStatusClosed {
		t.Fatalf("expected retry to actually close the trip, got %+v", closeRes)
	}
}

func TestClose_SuccessNoInternalGaps(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1},
		fixSpec{1000, 1, 1},
		fixSpec{2000, 1, 1},
	)
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID

	closeRes, err := svc.Close(ctxBg(), tripID, res.Revision, false)
	if err != nil {
		t.Fatalf("expected successful close, got %v", err)
	}
	if closeRes.Status != storage.TripStatusClosed || closeRes.CloseReason != storage.CloseReasonManual {
		t.Fatalf("unexpected close result: %+v", closeRes)
	}
	if closeRes.Revision != res.Revision+1 {
		t.Fatalf("expected revision bump, got %d", closeRes.Revision)
	}

	trip := getTrip(t, store, tripID)
	if trip.Status != storage.TripStatusClosed || trip.ClosedAtMs == nil {
		t.Fatalf("expected trip frozen closed, got %+v", trip)
	}
}

func TestClose_IncompleteRejectedWithoutForce(t *testing.T) {
	svc, store := newTestService(t)
	// Gap of 11000ms with 1000ms expected interval -> 10 missing internal fixes.
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1},
		fixSpec{11000, 1, 1},
	)
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID

	_, err := svc.Close(ctxBg(), tripID, res.Revision, false)
	if err == nil {
		t.Fatal("expected TRIP_INCOMPLETE error")
	}
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeTripIncomplete {
		t.Fatalf("expected TRIP_INCOMPLETE, got %v", err)
	}
	if ae.Details["requires_confirmation"] != true {
		t.Fatalf("expected requires_confirmation=true, got %+v", ae.Details)
	}
}

func TestClose_ForceSucceedsOnIncomplete(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1},
		fixSpec{11000, 1, 1},
	)
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID

	closeRes, err := svc.Close(ctxBg(), tripID, res.Revision, true)
	if err != nil {
		t.Fatalf("expected forced close to succeed, got %v", err)
	}
	if !closeRes.ForcedClose {
		t.Fatal("expected forced_close=true")
	}
	trip := getTrip(t, store, tripID)
	if !trip.ForcedClose {
		t.Fatal("expected trip row to record forced_close=true")
	}
}

func TestClose_StaleRevisionRejected(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1})
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID

	_, err := svc.Close(ctxBg(), tripID, res.Revision+1, false)
	if err == nil {
		t.Fatal("expected REVISION_CONFLICT")
	}
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeRevisionConflict {
		t.Fatalf("expected REVISION_CONFLICT, got %v", err)
	}
}

func TestClose_StaleRevisionRejectedEvenWithForce(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}, fixSpec{11000, 1, 1})
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID

	_, err := svc.Close(ctxBg(), tripID, res.Revision+1, true)
	if err == nil {
		t.Fatal("expected REVISION_CONFLICT even with force=true")
	}
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeRevisionConflict {
		t.Fatalf("expected REVISION_CONFLICT, got %v", err)
	}
}

func TestClose_RepeatedCloseIsIdempotentNoOp(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1})
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID

	first, err := svc.Close(ctxBg(), tripID, res.Revision, false)
	if err != nil {
		t.Fatal(err)
	}

	// Repeat with a stale expected_revision; must still succeed as no-op.
	second, err := svc.Close(ctxBg(), tripID, res.Revision, false)
	if err != nil {
		t.Fatalf("expected idempotent no-op, got error %v", err)
	}
	if second.Revision != first.Revision {
		t.Fatalf("expected same revision on repeat close, got %d vs %d", second.Revision, first.Revision)
	}
}

func TestClose_MergedTargetReportsSuccessor(t *testing.T) {
	svc, store := newTestService(t)
	pkt1 := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1},
		fixSpec{600_000, 1, 1},
		fixSpec{1_800_000, 1, 1},
		fixSpec{2_400_000, 1, 1},
	)
	mustIngest(t, svc, pkt1)
	mutable := listMutableTrips(t, store, "vehicle-1")
	firstTripID := mutable[0].TripID

	// Bridge at +1200s, connecting both existing trips into a new merged one.
	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{1_200_000, 1, 1})
	mustIngest(t, svc, pkt2)

	// firstTripID should now be CLOSED/MERGED.
	_, err := svc.Close(ctxBg(), firstTripID, 999, false)
	if err == nil {
		t.Fatal("expected TRIP_SUPERSEDED")
	}
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeTripSuperseded {
		t.Fatalf("expected TRIP_SUPERSEDED, got %v", err)
	}
	if ae.Details["successor_trip_id"] == nil {
		t.Fatal("expected successor_trip_id in details")
	}
}

func TestClose_ThenInternalLateFixQuarantinedFrozenSnapshot(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1},
		fixSpec{1000, 1, 1},
	)
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID
	closeRes, err := svc.Close(ctxBg(), tripID, res.Revision, false)
	if err != nil {
		t.Fatal(err)
	}

	frozenBefore := getTrip(t, store, tripID)

	// Late internal fix inside [start,end].
	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{500, 9, 9})
	res2 := mustIngest(t, svc, pkt2)
	if res2.QuarantinedFixes != 1 {
		t.Fatalf("expected 1 quarantined fix, got %d", res2.QuarantinedFixes)
	}

	frozenAfter := getTrip(t, store, tripID)
	if frozenAfter.FixCount != frozenBefore.FixCount {
		t.Fatalf("expected frozen trip fix_count unchanged, got %d vs %d", frozenAfter.FixCount, frozenBefore.FixCount)
	}
	if frozenAfter.MinLatitude != frozenBefore.MinLatitude || frozenAfter.MaxLatitude != frozenBefore.MaxLatitude {
		t.Fatalf("expected frozen trip geometry unchanged")
	}
	_ = closeRes
}

func TestClose_FixAfterEndStartsNewTripEvenWithinGap(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1})
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID
	svc.Close(ctxBg(), tripID, res.Revision, false)

	// Fix just 100ms after closed trip's end, well within trip_gap_seconds,
	// but must not bridge into the closed trip.
	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{100, 2, 2})
	mustIngest(t, svc, pkt2)

	mutableAfter := listMutableTrips(t, store, "vehicle-1")
	if len(mutableAfter) != 1 {
		t.Fatalf("expected 1 new mutable trip after closed boundary, got %d", len(mutableAfter))
	}
	if mutableAfter[0].TripID == tripID {
		t.Fatal("expected a new trip ID, not the closed trip")
	}

	closedTrip := getTrip(t, store, tripID)
	if closedTrip.FixCount != 1 {
		t.Fatalf("expected closed trip unchanged, got fix_count=%d", closedTrip.FixCount)
	}
}

func TestClose_NoBridgeAcrossBarrierEvenWithinGap(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1})
	res := mustIngest(t, svc, pkt)
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID
	svc.Close(ctxBg(), tripID, res.Revision, false)

	// Fixes before and after the barrier, both within gap of the barrier
	// edges but on opposite sides; they must never connect into one trip.
	pktBefore := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{-100, 2, 2})
	mustIngest(t, svc, pktBefore)
	pktAfter := buildPacket("sim-1", "vehicle-1", "20261024-000003", baseMs, fixSpec{100, 3, 3})
	mustIngest(t, svc, pktAfter)

	mutableAfter := listMutableTrips(t, store, "vehicle-1")
	if len(mutableAfter) != 2 {
		t.Fatalf("expected 2 separate trips on either side of the barrier, got %d", len(mutableAfter))
	}
}
