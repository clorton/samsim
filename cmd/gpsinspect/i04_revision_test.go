package main

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"gpsdemo/internal/inspectoutput"
)

// TestI04_RevisionConflict_RetriesWithinBudget verifies that a
// REVISION_CONFLICT on a pinned read discards the whole attempt and
// restarts from a fresh snapshot, succeeding once the server stabilizes
// within the configured attempt budget.
func TestI04_RevisionConflict_RetriesWithinBudget(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	vehicleID := "vehicle-001"
	fs.seedSimpleVehicle(vehicleID)

	var callCount int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vehicles/{id}/snapshot", fs.handleSnapshot)
	mux.HandleFunc("/api/v1/vehicles/{id}", fs.handleGetVehicle)
	mux.HandleFunc("/api/v1/vehicles/{id}/trips", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			// First attempt's pinned read conflicts; client must discard and retry.
			writeJSON(w, 409, errEnvelope("REVISION_CONFLICT", "stale", map[string]any{"revision": int64(1)}))
			return
		}
		fs.handleListTrips(w, r)
	})
	srv := newTestServerFromMux(mux)
	defer srv.Close()

	stdout, stderr, exit := runCLI(t, "", "--base-url", srv.URL, "--revision-attempts", "3", "trips", vehicleID)
	if exit != 0 {
		t.Fatalf("expected exit 0 after retry succeeds, got %d (stderr=%s)", exit, stderr)
	}
	if !strings.Contains(stdout, "trip-"+vehicleID) {
		t.Fatalf("expected trip in output after successful retry: %s", stdout)
	}
	if atomic.LoadInt32(&callCount) < 2 {
		t.Fatalf("expected at least 2 calls (one conflict, one success), got %d", callCount)
	}
}

// TestI04_RevisionConflict_ExhaustsBudget verifies that persistent
// REVISION_CONFLICT responses exhaust the configured attempt budget and
// return exit code 5.
func TestI04_RevisionConflict_ExhaustsBudget(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	vehicleID := "vehicle-001"
	fs.seedSimpleVehicle(vehicleID)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vehicles/{id}/snapshot", fs.handleSnapshot)
	mux.HandleFunc("/api/v1/vehicles/{id}", fs.handleGetVehicle)
	mux.HandleFunc("/api/v1/vehicles/{id}/trips", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 409, errEnvelope("REVISION_CONFLICT", "stale", map[string]any{"revision": int64(1)}))
	})
	srv := newTestServerFromMux(mux)
	defer srv.Close()

	_, stderr, exit := runCLI(t, "", "--base-url", srv.URL, "--revision-attempts", "2", "trips", vehicleID)
	if exit != inspectoutput.ExitRevisionExhausted {
		t.Fatalf("expected exit %d, got %d (stderr=%s)", inspectoutput.ExitRevisionExhausted, exit, stderr)
	}
}

// TestI04_DatabaseChangeMidCollection_NeverMixesObservations verifies that
// if the database_id changes between the initial snapshot and the final
// re-check, the whole attempt is discarded (and retried, here exhausting
// since it always changes).
func TestI04_DatabaseChangeMidCollection_NeverMixesObservations(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	vehicleID := "vehicle-001"
	fs.seedSimpleVehicle(vehicleID)

	var snapshotCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vehicles/{id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&snapshotCalls, 1)
		fs.mu.Lock()
		dbID := fs.databaseID
		if n > 1 {
			dbID = "a-different-database"
		}
		fs.mu.Unlock()
		v := fs.vehicles[vehicleID]
		writeJSON(w, 200, map[string]any{
			"vehicle": map[string]any{
				"vehicle_id": v.VehicleID, "latest_fix": v.LatestFix, "active_trip_id": v.ActiveTripID,
				"revision": v.Revision, "database_id": dbID,
				"trip_gap_seconds": v.TripGapSeconds, "expected_fix_interval_ms": v.ExpectedFixIntervalMs,
			},
			"trips": []map[string]any{}, "manual_barriers": []map[string]any{}, "revision": v.Revision, "database_id": dbID,
		})
	})
	mux.HandleFunc("/api/v1/vehicles/{id}", func(w http.ResponseWriter, r *http.Request) {
		// The recheck call always reports the ORIGINAL database_id changed
		// under it (simulated by always returning a fresh/different ID vs
		// what the snapshot just promised), forcing a restart every time.
		v := fs.vehicles[vehicleID]
		writeJSON(w, 200, map[string]any{
			"vehicle_id": v.VehicleID, "latest_fix": v.LatestFix, "active_trip_id": v.ActiveTripID,
			"revision": v.Revision, "database_id": "yet-another-database",
			"trip_gap_seconds": v.TripGapSeconds, "expected_fix_interval_ms": v.ExpectedFixIntervalMs,
		})
	})
	mux.HandleFunc("/api/v1/vehicles/{id}/trips", fs.handleListTrips)
	srv := newTestServerFromMux(mux)
	defer srv.Close()

	_, _, exit := runCLI(t, "", "--base-url", srv.URL, "--revision-attempts", "2", "trips", vehicleID)
	if exit != inspectoutput.ExitRevisionExhausted {
		t.Fatalf("expected exit %d (never settles, database keeps changing), got %d", inspectoutput.ExitRevisionExhausted, exit)
	}
}
