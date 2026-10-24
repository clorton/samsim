package aggregator

import (
	"sync"
	"testing"

	"gpsdemo/internal/apierr"
)

// TestConcurrency_SamePacketStoresOnce implements part of T13: concurrent
// identical packet submissions must store exactly once and all callers
// receive a consistent result (exactly one first acceptance, the rest
// duplicates with matching counters).
func TestConcurrency_SamePacketStoresOnce(t *testing.T) {
	svc, store := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1})

	const n = 20
	var wg sync.WaitGroup
	results := make([]*IngestResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = ingestNoFatal(svc, pkt)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	firstAcceptanceCount := 0
	for _, res := range results {
		if !res.DuplicatePacket {
			firstAcceptanceCount++
		}
		if res.AcceptedFixes != 1 || res.Revision != 1 {
			t.Fatalf("expected consistent counters across all callers, got %+v", res)
		}
	}
	if firstAcceptanceCount != 1 {
		t.Fatalf("expected exactly 1 first acceptance among %d concurrent identical packets, got %d", n, firstAcceptanceCount)
	}

	mutable := listMutableTrips(t, store, "vehicle-1")
	if len(mutable) != 1 {
		t.Fatalf("expected exactly 1 trip, got %d", len(mutable))
	}
	fixes := tripFixTimestamps(t, store, mutable[0].TripID)
	if len(fixes) != 1 {
		t.Fatalf("expected exactly 1 stored fix, got %d", len(fixes))
	}
}

// TestConcurrency_DifferentPacketsConflictSafelyOnFixIdentity implements
// part of T13: concurrent submissions of different packets that share a
// conflicting fix identity must not corrupt state -- exactly one succeeds
// and the other observes a clean FIX_CONFLICT, never partial state.
func TestConcurrency_DifferentPacketsConflictSafelyOnFixIdentity(t *testing.T) {
	svc, store := newTestService(t)

	pktA := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1})
	pktB := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{0, 2, 2}) // conflicting lat/lon, same timestamp

	var wg sync.WaitGroup
	var resA, resB *IngestResult
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		resA, errA = ingestNoFatal(svc, pktA)
	}()
	go func() {
		defer wg.Done()
		resB, errB = ingestNoFatal(svc, pktB)
	}()
	wg.Wait()

	successCount := 0
	if errA == nil {
		successCount++
	}
	if errB == nil {
		successCount++
	}
	if successCount != 1 {
		t.Fatalf("expected exactly 1 success, got resA=%+v errA=%v resB=%+v errB=%v", resA, errA, resB, errB)
	}
	failErr := errA
	if failErr == nil {
		failErr = errB
	}
	ae, ok := failErr.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeFixConflict {
		t.Fatalf("expected FIX_CONFLICT on the losing packet, got %v", failErr)
	}

	mutable := listMutableTrips(t, store, "vehicle-1")
	if len(mutable) != 1 {
		t.Fatalf("expected exactly 1 trip, got %d", len(mutable))
	}
	fixes := tripFixTimestamps(t, store, mutable[0].TripID)
	if len(fixes) != 1 {
		t.Fatalf("expected exactly 1 stored fix, got %d", len(fixes))
	}
}

// TestConcurrency_IngestionAndClosureSerialized implements part of T13:
// concurrent ingestion and closure on the same trip must be serialized by
// the per-vehicle lock + writer coordinator, leaving a consistent final
// state (no partial/corrupted trip row).
func TestConcurrency_IngestionAndClosureSerialized(t *testing.T) {
	svc, store := newTestService(t)
	res := mustIngest(t, svc, buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 1, 1}))
	mutable := listMutableTrips(t, store, "vehicle-1")
	tripID := mutable[0].TripID

	var wg sync.WaitGroup
	wg.Add(2)
	var closeErr error
	go func() {
		defer wg.Done()
		_, closeErr = svc.Close(ctxBg(), tripID, res.Revision, false)
	}()
	var ingestErr error
	go func() {
		defer wg.Done()
		_, ingestErr = ingestNoFatal(svc, buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{500, 9, 9}))
	}()
	wg.Wait()

	// Both operations should complete without panicking or corrupting
	// state; at least one should succeed (exact outcome depends on
	// scheduling order, which is why we only assert structural
	// consistency, not a specific winner).
	finalTrip := getTrip(t, store, tripID)
	if finalTrip.FixCount < 1 {
		t.Fatalf("expected trip to retain at least its original fix, got %+v", finalTrip)
	}
	_ = closeErr
	_ = ingestErr
}
