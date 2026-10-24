package aggregator

import (
	"fmt"
	"testing"

	"gpsdemo/internal/apierr"
)

func TestListVehicles_Basic(t *testing.T) {
	svc, _ := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}))
	mustIngest(t, svc, buildPacket("sim-2", "vehicle-2", "20261024-000001", baseMs, fixSpec{0, 1, 1}))

	env, err := svc.ListVehicles(ctxBg(), ListVehiclesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Items) != 2 {
		t.Fatalf("expected 2 vehicles, got %d", len(env.Items))
	}
	if env.Items[0].VehicleID != "vehicle-1" || env.Items[1].VehicleID != "vehicle-2" {
		t.Fatalf("expected ascending vehicle_id order, got %+v", env.Items)
	}
}

func TestListVehicles_ActiveOnly(t *testing.T) {
	svc, store := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}))
	res := mustIngest(t, svc, buildPacket("sim-2", "vehicle-2", "20261024-000001", baseMs, fixSpec{0, 1, 1}))
	mutable := listMutableTrips(t, store, "vehicle-2")
	svc.Close(ctxBg(), mutable[0].TripID, res.Revision, false)

	env, err := svc.ListVehicles(ctxBg(), ListVehiclesParams{ActiveOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Items) != 1 || env.Items[0].VehicleID != "vehicle-1" {
		t.Fatalf("expected only vehicle-1 active, got %+v", env.Items)
	}
}

func TestListVehicles_Pagination(t *testing.T) {
	svc, _ := newTestService(t)
	for i, id := range []string{"vehicle-a", "vehicle-b", "vehicle-c"} {
		mustIngest(t, svc, buildPacket(fmt.Sprintf("sim-%d", i), id, "20261024-000001", baseMs, fixSpec{0, 1, 1}))
	}

	page1, err := svc.ListVehicles(ctxBg(), ListVehiclesParams{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 2 || page1.NextCursor == nil {
		t.Fatalf("expected 2 items + next cursor, got %+v", page1)
	}

	page2, err := svc.ListVehicles(ctxBg(), ListVehiclesParams{Limit: 2, Cursor: *page1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 || page2.NextCursor != nil {
		t.Fatalf("expected final page with 1 item, got %+v", page2)
	}
}

func TestGetVehicle_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.GetVehicle(ctxBg(), "nonexistent", nil)
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

func TestGetVehicle_AtRevisionMismatch(t *testing.T) {
	svc, _ := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}))

	stale := int64(999)
	_, err := svc.GetVehicle(ctxBg(), "vehicle-1", &stale)
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeRevisionConflict {
		t.Fatalf("expected REVISION_CONFLICT, got %v", err)
	}
}

func TestGetSnapshot_CoherentView(t *testing.T) {
	svc, _ := newTestService(t)
	res := mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1}, fixSpec{1000, 1, 1}))

	snap, err := svc.GetSnapshot(ctxBg(), "vehicle-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Revision != res.Revision {
		t.Fatalf("expected snapshot revision %d, got %d", res.Revision, snap.Revision)
	}
	if len(snap.Trips) != 1 {
		t.Fatalf("expected 1 trip in snapshot, got %d", len(snap.Trips))
	}
	if snap.Vehicle.VehicleID != "vehicle-1" {
		t.Fatalf("unexpected vehicle in snapshot: %+v", snap.Vehicle)
	}
	if snap.DatabaseID == "" {
		t.Fatal("expected nonempty database_id")
	}
}

func TestGetSnapshot_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.GetSnapshot(ctxBg(), "nonexistent")
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

func TestListTrips_IncludeMerged(t *testing.T) {
	svc, _ := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1}, fixSpec{600_000, 1, 1}, fixSpec{1_800_000, 1, 1}, fixSpec{2_400_000, 1, 1}))
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{1_200_000, 1, 1}))

	withoutMerged, err := svc.ListTrips(ctxBg(), "vehicle-1", ListTripsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutMerged.Items) != 1 {
		t.Fatalf("expected 1 current trip, got %d", len(withoutMerged.Items))
	}

	withMerged, err := svc.ListTrips(ctxBg(), "vehicle-1", ListTripsParams{IncludeMerged: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(withMerged.Items) != 3 {
		t.Fatalf("expected 3 trips (1 current + 2 merged predecessors), got %d", len(withMerged.Items))
	}
}

func TestListTrips_StatusFilter(t *testing.T) {
	svc, _ := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}))
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{10_000_000, 1, 1}))

	active, err := svc.ListTrips(ctxBg(), "vehicle-1", ListTripsParams{Status: "ACTIVE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(active.Items) != 1 {
		t.Fatalf("expected 1 active trip, got %d", len(active.Items))
	}

	inactive, err := svc.ListTrips(ctxBg(), "vehicle-1", ListTripsParams{Status: "INACTIVE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inactive.Items) != 1 {
		t.Fatalf("expected 1 inactive trip, got %d", len(inactive.Items))
	}
}

func TestListVehicleFixes_AssignmentFilter(t *testing.T) {
	svc, store := newTestService(t)
	res := mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}, fixSpec{1000, 1, 1}))
	mutable := listMutableTrips(t, store, "vehicle-1")
	svc.Close(ctxBg(), mutable[0].TripID, res.Revision, false)
	// Interior late fix within the closed barrier [0,1000] -> quarantined.
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{500, 9, 9}))

	all, err := svc.ListVehicleFixes(ctxBg(), "vehicle-1", ListFixesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 3 {
		t.Fatalf("expected 3 fixes total, got %d", len(all.Items))
	}

	quarantined, err := svc.ListVehicleFixes(ctxBg(), "vehicle-1", ListFixesParams{Assignment: "quarantined"})
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantined.Items) != 1 {
		t.Fatalf("expected 1 quarantined fix, got %d", len(quarantined.Items))
	}
}

func TestGetTripDetail_MergeLineage(t *testing.T) {
	svc, store := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1}, fixSpec{600_000, 1, 1}, fixSpec{1_800_000, 1, 1}, fixSpec{2_400_000, 1, 1}))
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{1_200_000, 1, 1}))

	mutable := listMutableTrips(t, store, "vehicle-1")
	mergedTripID := mutable[0].TripID

	detail, err := svc.GetTripDetail(ctxBg(), mergedTripID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.ImmediatePredecessorTripIDs) != 2 {
		t.Fatalf("expected 2 predecessors, got %+v", detail.ImmediatePredecessorTripIDs)
	}
	if detail.Completeness == nil {
		t.Fatal("expected completeness assessment")
	}
}

func TestGetTripDetail_ResolvesTerminalSuccessor(t *testing.T) {
	svc, store := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1}, fixSpec{600_000, 1, 1}, fixSpec{1_800_000, 1, 1}, fixSpec{2_400_000, 1, 1}))
	mutableBefore := listMutableTrips(t, store, "vehicle-1")
	firstTripID := mutableBefore[0].TripID

	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{1_200_000, 1, 1}))

	detail, err := svc.GetTripDetail(ctxBg(), firstTripID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ImmediateSuccessorTripID == nil {
		t.Fatal("expected immediate successor on merged predecessor")
	}
	if detail.TerminalSuccessorTripID == nil {
		t.Fatal("expected terminal successor on merged predecessor")
	}
}

func TestGetTripFixes_MergedPredecessorGeometryPreserved(t *testing.T) {
	svc, store := newTestService(t)
	// Two separate trips, 2 fixes each.
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1}, fixSpec{600_000, 1, 1},
		fixSpec{1_800_000, 1, 1}, fixSpec{2_400_000, 1, 1}))
	mutable := listMutableTrips(t, store, "vehicle-1")
	firstTripID := mutable[0].TripID
	firstTripFixCountBefore := len(tripFixTimestamps(t, store, firstTripID))

	// Bridge at +1200s merges both into a new trip.
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{1_200_000, 1, 1}))

	// firstTripID is now a frozen CLOSED/MERGED predecessor; its own
	// snapshot geometry must be unchanged by the merge.
	fixes, err := svc.GetTripFixes(ctxBg(), firstTripID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixes.Items) != firstTripFixCountBefore {
		t.Fatalf("expected frozen predecessor geometry with %d fixes, got %d", firstTripFixCountBefore, len(fixes.Items))
	}
}

func TestHealth_OK(t *testing.T) {
	svc, _ := newTestService(t)
	h, err := svc.Health(ctxBg())
	if err != nil {
		t.Fatal(err)
	}
	if !h.Healthy || h.DatabaseID == "" {
		t.Fatalf("unexpected health: %+v", h)
	}
}
