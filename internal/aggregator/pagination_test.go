package aggregator

import (
	"testing"

	"gpsdemo/internal/apierr"
)

// TestListTrips_StaleCursorRejected implements part of T15: a cursor issued
// against an earlier revision must be rejected once ingestion has advanced
// the vehicle's revision.
func TestListTrips_StaleCursorRejected(t *testing.T) {
	svc, _ := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1}, fixSpec{10_000_000, 1, 1}))

	page1, err := svc.ListTrips(ctxBg(), "vehicle-1", ListTripsParams{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page1.NextCursor == nil {
		t.Fatal("expected a next cursor for a 2-trip result with limit 1")
	}

	// Advance the vehicle's revision with new telemetry.
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{20_000_000, 1, 1}))

	_, err = svc.ListTrips(ctxBg(), "vehicle-1", ListTripsParams{Limit: 1, Cursor: *page1.NextCursor})
	if err == nil {
		t.Fatal("expected stale cursor to be rejected")
	}
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeRevisionConflict {
		t.Fatalf("expected REVISION_CONFLICT, got %v", err)
	}
}

// TestListVehicleFixes_StaleCursorRejected mirrors the above for the fixes
// list endpoint.
func TestListVehicleFixes_StaleCursorRejected(t *testing.T) {
	svc, _ := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}, fixSpec{1000, 1, 1}))

	page1, err := svc.ListVehicleFixes(ctxBg(), "vehicle-1", ListFixesParams{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page1.NextCursor == nil {
		t.Fatal("expected a next cursor")
	}

	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{2000, 1, 1}))

	_, err = svc.ListVehicleFixes(ctxBg(), "vehicle-1", ListFixesParams{Limit: 1, Cursor: *page1.NextCursor})
	if err == nil {
		t.Fatal("expected stale cursor to be rejected")
	}
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeRevisionConflict {
		t.Fatalf("expected REVISION_CONFLICT, got %v", err)
	}
}

// TestListTrips_CursorProgression implements the positive-path part of
// T15's pagination coverage: paging through with a stable revision yields
// all items exactly once with no overlap/gaps.
func TestListTrips_CursorProgression(t *testing.T) {
	svc, _ := newTestService(t)
	mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1}, fixSpec{10_000_000, 1, 1}, fixSpec{20_000_000, 1, 1}))

	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 10; i++ {
		page, err := svc.ListTrips(ctxBg(), "vehicle-1", ListTripsParams{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if seen[item.TripID] {
				t.Fatalf("duplicate trip_id %s across pages", item.TripID)
			}
			seen[item.TripID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 total trips paged, got %d", len(seen))
	}
}

// TestGetVehicle_AtRevisionSuccess verifies the positive path: a
// non-stale at_revision precondition succeeds.
func TestGetVehicle_AtRevisionSuccess(t *testing.T) {
	svc, _ := newTestService(t)
	res := mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}))

	v, err := svc.GetVehicle(ctxBg(), "vehicle-1", &res.Revision)
	if err != nil {
		t.Fatalf("expected at_revision match to succeed, got %v", err)
	}
	if v.Revision != res.Revision {
		t.Fatalf("unexpected revision: %+v", v)
	}
}
