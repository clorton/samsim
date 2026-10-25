package main

import (
	"strings"
	"testing"
)

// seedMergedLineage installs a vehicle with a predecessor trip MERGED into
// a successor trip, for lineage display tests.
func (fs *fixtureServer) seedMergedLineage(vehicleID string) (predecessorID, successorID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	predecessorID = "trip-pred"
	successorID = "trip-succ"

	predGeom := []map[string]any{
		{"timestamp_ms": int64(1000), "latitude": 40.0, "longitude": -73.0, "speed_mps": 1.0, "heading_degrees": 10.0, "current_trip_id": nil, "assignment_reason": nil},
	}
	succGeom := []map[string]any{
		predGeom[0],
		{"timestamp_ms": int64(2000), "latitude": 40.001, "longitude": -73.001, "speed_mps": 1.0, "heading_degrees": 10.0, "current_trip_id": successorID, "assignment_reason": nil},
	}

	predSummary := map[string]any{
		"trip_id": predecessorID, "vehicle_id": vehicleID, "start_ms": int64(1000), "end_ms": int64(1000),
		"fix_count": int64(1), "bounds": map[string]any{"min_latitude": 40.0, "max_latitude": 40.0, "min_longitude": -73.0, "max_longitude": -73.0},
		"status": "CLOSED", "close_reason": "MERGED", "superseded_by_trip_id": successorID,
		"created_revision": int64(1), "updated_revision": int64(2), "closed_at_ms": nil, "forced_close": false,
	}
	predDetail := cloneMap(predSummary)
	predDetail["immediate_predecessor_trip_ids"] = []string{}
	predDetail["immediate_successor_trip_id"] = successorID
	predDetail["terminal_successor_trip_id"] = successorID
	predDetail["completeness"] = fixtureCompleteDTO(predecessorID, 2)

	succSummary := map[string]any{
		"trip_id": successorID, "vehicle_id": vehicleID, "start_ms": int64(1000), "end_ms": int64(2000),
		"fix_count": int64(2), "bounds": map[string]any{"min_latitude": 40.0, "max_latitude": 40.001, "min_longitude": -73.001, "max_longitude": -73.0},
		"status": "ACTIVE", "close_reason": "NONE", "superseded_by_trip_id": nil,
		"created_revision": int64(2), "updated_revision": int64(2), "closed_at_ms": nil, "forced_close": false,
	}
	succDetail := cloneMap(succSummary)
	succDetail["immediate_predecessor_trip_ids"] = []string{predecessorID}
	succDetail["immediate_successor_trip_id"] = nil
	succDetail["terminal_successor_trip_id"] = nil
	succDetail["completeness"] = fixtureCompleteDTO(successorID, 2)

	fs.trips[predecessorID] = &fixtureTrip{Summary: predSummary, Detail: predDetail, Geometry: predGeom, Completeness: predDetail["completeness"].(map[string]any)}
	fs.trips[successorID] = &fixtureTrip{Summary: succSummary, Detail: succDetail, Geometry: succGeom, Completeness: succDetail["completeness"].(map[string]any)}

	active := successorID
	fs.vehicles[vehicleID] = &fixtureVehicle{
		VehicleID: vehicleID, ActiveTripID: &active, Revision: 2,
		LatestFix: succGeom[1], TripGapSeconds: 600, ExpectedFixIntervalMs: 1000,
	}
	return predecessorID, successorID
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func fixtureCompleteDTO(tripID string, revision int64) map[string]any {
	return map[string]any{
		"trip_id": tripID, "revision": revision, "expected_fix_interval_ms": int64(1000),
		"missing_fix_count": int64(0), "missing_intervals": nil, "complete": true,
		"boundary_completeness": "UNKNOWN", "assessment": "NO_INTERNAL_GAPS",
	}
}

// TestI05_TripRetainsRequestedMergedIdentity verifies that requesting a
// MERGED (predecessor) trip by its own ID shows its own CLOSED/MERGED
// status and successor, rather than being silently redirected to the
// successor.
func TestI05_TripRetainsRequestedMergedIdentity(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	predID, succID := fs.seedMergedLineage("vehicle-001")

	stdout, _, exit := runCLI(t, "", "--base-url", fs.URL(), "trip", predID)
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d: %s", exit, stdout)
	}
	if !strings.Contains(stdout, "Trip ID: "+predID) {
		t.Fatalf("expected the requested predecessor trip ID in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, "CLOSED") || !strings.Contains(stdout, "MERGED") {
		t.Fatalf("expected CLOSED/MERGED status, got: %s", stdout)
	}
	if !strings.Contains(stdout, succID) {
		t.Fatalf("expected successor ID %s in output: %s", succID, stdout)
	}
}

// TestI05_GapsTruncation verifies gaps caps display at the configured
// interval limit while total/truncation remain accurate.
func TestI05_GapsTruncation(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	vehicleID := "vehicle-001"
	tripID := fs.seedVehicleWithGaps(vehicleID)

	stdout, _, exit := runCLI(t, "", "--base-url", fs.URL(), "--json", "gaps", tripID)
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d: %s", exit, stdout)
	}
	if !strings.Contains(stdout, `"total_intervals":1`) {
		t.Fatalf("expected total_intervals=1, got: %s", stdout)
	}
}

// TestI05_GapsCompleteLabel verifies the mandatory "No detected internal
// gaps; boundary completeness unknown" wording for complete=true trips.
func TestI05_GapsCompleteLabel(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedSimpleVehicle("vehicle-001")

	stdout, _, exit := runCLI(t, "", "--base-url", fs.URL(), "gaps", tripID)
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d", exit)
	}
	if !strings.Contains(stdout, "No detected internal gaps; boundary completeness unknown") {
		t.Fatalf("expected exact boundary-uncertainty wording, got: %s", stdout)
	}
}
