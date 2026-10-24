package aggregator

import (
	"context"
	"database/sql"
	"testing"

	"gpsdemo/internal/storage"
)

func getVehicle(t *testing.T, store *storage.Store, vehicleID string) *storage.VehicleRow {
	t.Helper()
	v, err := storage.ReadTx(context.Background(), store, func(ctx context.Context, tx *sql.Tx) (*storage.VehicleRow, error) {
		return storage.GetVehicle(ctx, tx, vehicleID)
	})
	if err != nil {
		t.Fatalf("get vehicle: %v", err)
	}
	return v
}

func getTrip(t *testing.T, store *storage.Store, tripID string) *storage.TripRow {
	t.Helper()
	tr, err := storage.ReadTx(context.Background(), store, func(ctx context.Context, tx *sql.Tx) (*storage.TripRow, error) {
		return storage.GetTrip(ctx, tx, tripID)
	})
	if err != nil {
		t.Fatalf("get trip %s: %v", tripID, err)
	}
	return tr
}

func listMutableTrips(t *testing.T, store *storage.Store, vehicleID string) []storage.TripRow {
	t.Helper()
	trips, err := storage.ReadTx(context.Background(), store, func(ctx context.Context, tx *sql.Tx) ([]storage.TripRow, error) {
		return storage.ListMutableTripsByVehicle(ctx, tx, vehicleID)
	})
	if err != nil {
		t.Fatalf("list mutable trips: %v", err)
	}
	return trips
}

func listManualBarriers(t *testing.T, store *storage.Store, vehicleID string) []storage.ManualBarrierRow {
	t.Helper()
	barriers, err := storage.ReadTx(context.Background(), store, func(ctx context.Context, tx *sql.Tx) ([]storage.ManualBarrierRow, error) {
		return storage.ListManualBarriersByVehicle(ctx, tx, vehicleID)
	})
	if err != nil {
		t.Fatalf("list manual barriers: %v", err)
	}
	return barriers
}

func tripFixTimestamps(t *testing.T, store *storage.Store, tripID string) []int64 {
	t.Helper()
	ts, err := storage.ReadTx(context.Background(), store, func(ctx context.Context, tx *sql.Tx) ([]int64, error) {
		return storage.ListTripFixTimestamps(ctx, tx, tripID)
	})
	if err != nil {
		t.Fatalf("list trip fix timestamps: %v", err)
	}
	return ts
}

// TestReconstruct_BasicSegmentationAndMerge implements T06: ingest
// T+{0,600,1800,2400}s as two trips, then +1200s bridges them into one new
// merged trip with correct union/lineage.
func TestReconstruct_BasicSegmentationAndMerge(t *testing.T) {
	svc, store := newTestService(t)

	pkt1 := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1},
		fixSpec{600_000, 1, 1},
		fixSpec{1_800_000, 1, 1},
		fixSpec{2_400_000, 1, 1},
	)
	res1 := mustIngest(t, svc, pkt1)
	if res1.TripUpdates != 2 {
		t.Fatalf("expected 2 trips created, got %d", res1.TripUpdates)
	}

	mutable := listMutableTrips(t, store, "vehicle-1")
	if len(mutable) != 2 {
		t.Fatalf("expected 2 mutable trips, got %d", len(mutable))
	}

	// Second trip (later start) should be ACTIVE; first should be INACTIVE.
	var activeCount, inactiveCount int
	for _, tr := range mutable {
		if tr.Status == storage.TripStatusActive {
			activeCount++
		} else if tr.Status == storage.TripStatusInactive {
			inactiveCount++
		}
	}
	if activeCount != 1 || inactiveCount != 1 {
		t.Fatalf("expected 1 active + 1 inactive, got active=%d inactive=%d", activeCount, inactiveCount)
	}

	// Bridge with a fix at +1200s.
	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{1_200_000, 1, 1})
	res2 := mustIngest(t, svc, pkt2)
	// 1 new trip + 2 predecessors closed = 3 trip_updates.
	if res2.TripUpdates != 3 {
		t.Fatalf("expected 3 trip updates (merge), got %d", res2.TripUpdates)
	}

	mutableAfter := listMutableTrips(t, store, "vehicle-1")
	if len(mutableAfter) != 1 {
		t.Fatalf("expected 1 mutable trip after merge, got %d", len(mutableAfter))
	}
	merged := mutableAfter[0]
	if merged.Status != storage.TripStatusActive {
		t.Fatalf("expected merged trip to be ACTIVE, got %s", merged.Status)
	}
	if merged.FixCount != 5 {
		t.Fatalf("expected 5 fixes in merged trip, got %d", merged.FixCount)
	}

	ts := tripFixTimestamps(t, store, merged.TripID)
	if len(ts) != 5 {
		t.Fatalf("expected 5 member timestamps, got %d", len(ts))
	}

	// Both original trips must now be CLOSED/MERGED, pointing to the new trip.
	for _, oldID := range []string{mutable[0].TripID, mutable[1].TripID} {
		old := getTrip(t, store, oldID)
		if old.Status != storage.TripStatusClosed || old.CloseReason != storage.CloseReasonMerged {
			t.Fatalf("expected old trip %s CLOSED/MERGED, got %s/%s", oldID, old.Status, old.CloseReason)
		}
		if old.SupersededByTripID == nil || *old.SupersededByTripID != merged.TripID {
			t.Fatalf("expected old trip %s superseded by %s, got %v", oldID, merged.TripID, old.SupersededByTripID)
		}
	}

	v := getVehicle(t, store, "vehicle-1")
	if v.ActiveTripID == nil || *v.ActiveTripID != merged.TripID {
		t.Fatalf("expected vehicle active trip to be merged trip, got %v", v.ActiveTripID)
	}
}

// TestReconstruct_GapBoundaries implements T05's boundary cases: 599999 and
// 600000 connect; 600001 splits.
func TestReconstruct_GapBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		gapMs     int64
		wantTrips int
	}{
		{"599999_connects", 599_999, 1},
		{"600000_connects_inclusive", 600_000, 1},
		{"600001_splits", 600_001, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, store := newTestService(t)
			pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
				fixSpec{0, 1, 1},
				fixSpec{c.gapMs, 1, 1},
			)
			mustIngest(t, svc, pkt)
			mutable := listMutableTrips(t, store, "vehicle-1")
			if len(mutable) != c.wantTrips {
				t.Fatalf("gap=%d: expected %d trips, got %d", c.gapMs, c.wantTrips, len(mutable))
			}
		})
	}
}

// TestReconstruct_LatestFirstThenOlderNeverRegressesPosition implements part
// of T05: ingesting a later fix first, then an older fix, must never
// regress latest position.
func TestReconstruct_LatestFirstThenOlderNeverRegressesPosition(t *testing.T) {
	svc, store := newTestService(t)

	pktLater := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{10_000, 1, 1})
	mustIngest(t, svc, pktLater)
	v1 := getVehicle(t, store, "vehicle-1")
	if v1.LatestFixTimestampMs == nil || *v1.LatestFixTimestampMs != baseMs+10_000 {
		t.Fatalf("unexpected latest position after first ingest: %v", v1.LatestFixTimestampMs)
	}

	pktOlder := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{0, 1, 1})
	mustIngest(t, svc, pktOlder)
	v2 := getVehicle(t, store, "vehicle-1")
	if v2.LatestFixTimestampMs == nil || *v2.LatestFixTimestampMs != baseMs+10_000 {
		t.Fatalf("expected latest position to remain at later fix, got %v", v2.LatestFixTimestampMs)
	}
}

// TestReconstruct_NewDisconnectedTripDeactivatesPrior implements T09: a new
// disconnected latest trip deactivates the prior active trip; no timeout
// needed.
func TestReconstruct_NewDisconnectedTripDeactivatesPrior(t *testing.T) {
	svc, store := newTestService(t)

	pkt1 := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1})
	mustIngest(t, svc, pkt1)
	mutable1 := listMutableTrips(t, store, "vehicle-1")
	if len(mutable1) != 1 || mutable1[0].Status != storage.TripStatusActive {
		t.Fatalf("expected 1 active trip, got %+v", mutable1)
	}
	firstTripID := mutable1[0].TripID

	// Far away in time (> gap), creating a new disconnected trip.
	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{10_000_000, 1, 1})
	mustIngest(t, svc, pkt2)

	first := getTrip(t, store, firstTripID)
	if first.Status != storage.TripStatusInactive {
		t.Fatalf("expected first trip to become INACTIVE, got %s", first.Status)
	}

	mutable2 := listMutableTrips(t, store, "vehicle-1")
	var activeCount int
	for _, tr := range mutable2 {
		if tr.Status == storage.TripStatusActive {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("expected exactly 1 active trip, got %d", activeCount)
	}
}

// TestReconstruct_LateOldFixDoesNotReactivate implements more of T09: adding
// a late fix to an older INACTIVE trip must not reactivate it while a newer
// trip remains ACTIVE.
func TestReconstruct_LateOldFixDoesNotReactivate(t *testing.T) {
	svc, store := newTestService(t)

	pkt1 := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1})
	mustIngest(t, svc, pkt1)
	mutable1 := listMutableTrips(t, store, "vehicle-1")
	firstTripID := mutable1[0].TripID

	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{10_000_000, 1, 1})
	mustIngest(t, svc, pkt2)

	// Add an internal fix to the first (now inactive) trip's neighborhood,
	// still far from the active trip.
	pkt3 := buildPacket("sim-1", "vehicle-1", "20261024-000003", baseMs, fixSpec{500, 1, 1})
	mustIngest(t, svc, pkt3)

	first := getTrip(t, store, firstTripID)
	if first.Status != storage.TripStatusInactive {
		t.Fatalf("expected first trip to remain INACTIVE, got %s", first.Status)
	}

	v := getVehicle(t, store, "vehicle-1")
	if v.ActiveTripID == nil || *v.ActiveTripID == firstTripID {
		t.Fatalf("expected active trip to remain the later trip, got %v", v.ActiveTripID)
	}
}
