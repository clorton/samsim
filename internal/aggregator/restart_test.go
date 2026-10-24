package aggregator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gpsdemo/internal/clock"
	"gpsdemo/internal/events"
	"gpsdemo/internal/idgen"
	"gpsdemo/internal/storage"
)

// TestRestart_PrecommitFailureLeavesNoTrace implements T14's "inject
// precommit failure -> no packet/fix/trip/revision/event" case: a failure
// staged after all writes are prepared but before the transaction commits
// must roll back completely, leaving the vehicle, trip and packet tables
// exactly as they were, and must not publish an event.
func TestRestart_PrecommitFailureLeavesNoTrace(t *testing.T) {
	svc, store := newTestService(t)

	// Establish a baseline committed state so we can verify it is untouched.
	baseline := mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}))

	sub := svc.Hub.Subscribe("vehicle-1")
	defer svc.Hub.Unsubscribe(sub)

	injected := errors.New("simulated precommit failure")
	store.SetPrecommitFailureHook(func() error { return injected })

	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{1000, 2, 2})
	err := ingestExpectingErr(t, svc, pkt)
	store.SetPrecommitFailureHook(nil) // clear immediately so later operations in this test are unaffected

	if err == nil {
		t.Fatal("expected injected precommit failure to surface as an error")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("expected the injected error to propagate, got %v", err)
	}

	// Vehicle revision and trip count must be exactly as before the failed
	// attempt.
	vehicle := getVehicle(t, store, "vehicle-1")
	if vehicle.Revision != baseline.Revision {
		t.Fatalf("expected revision unchanged at %d, got %d", baseline.Revision, vehicle.Revision)
	}
	mutable := listMutableTrips(t, store, "vehicle-1")
	if len(mutable) != 1 {
		t.Fatalf("expected still exactly 1 trip, got %d", len(mutable))
	}

	// The new fix must be entirely absent (rolled back along with the rest
	// of the transaction).
	members := tripFixTimestamps(t, store, mutable[0].TripID)
	if len(members) != 1 {
		t.Fatalf("expected trip to retain only its original fix, got %d members", len(members))
	}

	// The packet receipt must not have been stored either (so a client
	// retry after the crash would be treated as a fresh attempt, not a
	// duplicate of a half-applied one, and must succeed normally).
	retryResult := mustIngest(t, svc, pkt)
	if retryResult.DuplicatePacket {
		t.Fatal("expected retry after rolled-back precommit failure to be a fresh first acceptance, not a duplicate")
	}
	if retryResult.Revision != baseline.Revision+1 {
		t.Fatalf("expected retry to advance revision to %d, got %d", baseline.Revision+1, retryResult.Revision)
	}

	// No event must have been published for the failed attempt.
	select {
	case env := <-sub.Envelopes():
		if env.Revision != baseline.Revision+1 {
			t.Fatalf("unexpected envelope revision %d; it should correspond only to the successful retry, not the failed attempt", env.Revision)
		}
	default:
		t.Fatal("expected the successful retry's event to be available")
	}
}

// TestRestart_RetryAfterLostResponseIsIdempotent implements part of T14:
// simulating a client that successfully committed but never saw the
// response (e.g. connection dropped) by simply calling Ingest twice with
// the identical packet; the second call must return the stored original
// result without reprocessing or incrementing revision again.
func TestRestart_RetryAfterLostResponseIsIdempotent(t *testing.T) {
	svc, _ := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}, fixSpec{1000, 1, 1})

	first := mustIngest(t, svc, pkt)
	// Simulate "lost response": client never saw `first`, retries the
	// identical request.
	retry := mustIngest(t, svc, pkt)

	if !retry.DuplicatePacket {
		t.Fatal("expected retry to be recognized as duplicate_packet")
	}
	if retry.Revision != first.Revision {
		t.Fatalf("expected same revision on retry, got %d vs %d", retry.Revision, first.Revision)
	}
	if retry.AcceptedFixes != first.AcceptedFixes || retry.TripUpdates != first.TripUpdates {
		t.Fatalf("expected identical counters on retry: first=%+v retry=%+v", first, retry)
	}
}

// TestRestart_ReopenDatabaseRetainsLineageBarriersConfigRevisions implements
// the restart portion of T14: close a trip (creating lineage via merge and
// a manual barrier), restart the Store against the same file, and verify
// all state survives: lineage, barriers, config, and revisions.
func TestRestart_ReopenDatabaseRetainsLineageBarriersConfigRevisions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sqlite")

	openStore := func() *storage.Store {
		s, err := storage.Open(context.Background(), storage.Options{
			Path: path, BusyTimeoutMs: 5000, TripGapSeconds: 600, ExpectedFixIntervalMs: 1000,
		})
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		return s
	}

	store1 := openStore()
	hub := events.NewHub(16)
	clk := clock.NewFixed(time.Unix(1700000000, 0).UTC())
	ids := idgen.NewSequential("trip-")
	svc1 := NewService(store1, hub, clk, ids, 10000)

	// Create a merge (lineage) and a manual closure (barrier).
	mustIngest(t, svc1, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 1, 1}, fixSpec{600_000, 1, 1}, fixSpec{1_800_000, 1, 1}, fixSpec{2_400_000, 1, 1}))
	mustIngest(t, svc1, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{1_200_000, 1, 1}))

	mutable := listMutableTrips(t, store1, "vehicle-1")
	mergedTripID := mutable[0].TripID
	detailBeforeRestart, err := svc1.GetTripDetail(context.Background(), mergedTripID, nil)
	if err != nil {
		t.Fatal(err)
	}
	closeRes, err := svc1.Close(context.Background(), mergedTripID, detailBeforeRestart.UpdatedRevision, true)
	if err != nil {
		t.Fatal(err)
	}

	databaseIDBefore := store1.DatabaseID
	tripGapBefore := store1.TripGapSeconds
	expectedIntervalBefore := store1.ExpectedFixIntervalMs
	revisionBefore := closeRes.Revision

	store1.Close()

	// Restart against the same file.
	store2 := openStore()
	defer store2.Close()
	svc2 := NewService(store2, hub, clk, ids, 10000)

	if store2.DatabaseID != databaseIDBefore {
		t.Fatalf("expected same database_id after restart, got %s vs %s", store2.DatabaseID, databaseIDBefore)
	}
	if store2.TripGapSeconds != tripGapBefore || store2.ExpectedFixIntervalMs != expectedIntervalBefore {
		t.Fatalf("expected config to survive restart")
	}

	vehicle := getVehicle(t, store2, "vehicle-1")
	if vehicle.Revision != revisionBefore {
		t.Fatalf("expected revision %d to survive restart, got %d", revisionBefore, vehicle.Revision)
	}

	// Lineage: the merged trip's predecessors must still resolve.
	detailAfterRestart, err := svc2.GetTripDetail(context.Background(), mergedTripID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(detailAfterRestart.ImmediatePredecessorTripIDs) != 2 {
		t.Fatalf("expected lineage to survive restart, got %+v", detailAfterRestart.ImmediatePredecessorTripIDs)
	}

	// Barrier: the trip must still be CLOSED/MANUAL, and a fix inside its
	// interval must still be quarantined (proving the barrier survived).
	if detailAfterRestart.Status != storage.TripStatusClosed || detailAfterRestart.CloseReason != storage.CloseReasonManual {
		t.Fatalf("expected trip to remain CLOSED/MANUAL after restart, got %+v", detailAfterRestart.TripSummary)
	}
	lateInternalFix := buildPacket("sim-1", "vehicle-1", "20261024-000003", baseMs, fixSpec{1_000_000, 9, 9})
	resAfterRestart := mustIngest(t, svc2, lateInternalFix)
	if resAfterRestart.QuarantinedFixes != 1 {
		t.Fatalf("expected manual barrier to survive restart (fix should quarantine), got %+v", resAfterRestart)
	}
}
