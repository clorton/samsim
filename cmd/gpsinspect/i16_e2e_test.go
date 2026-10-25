package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gpsdemo/internal/aggregator"
	"gpsdemo/internal/clock"
	"gpsdemo/internal/events"
	"gpsdemo/internal/httpapi"
	"gpsdemo/internal/idgen"
	"gpsdemo/internal/storage"
)

// newRealAggregator spins up a real aggregator (storage + service + HTTP)
// against a temp SQLite file, for a true end-to-end gpsinspect test (INS-06
// I16): "inspect and validate outage/merge/forced-close scenarios without
// opening SQLite." This test only ever touches the aggregator through
// gpsinspect's public commands and plain HTTP telemetry POSTs (mimicking
// exactly what the simulator sends over the wire), never storage/aggregator
// package internals directly.
func newRealAggregator(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(context.Background(), storage.Options{
		Path:                  filepath.Join(dir, "test.sqlite"),
		BusyTimeoutMs:         5000,
		TripGapSeconds:        600,
		ExpectedFixIntervalMs: 1000,
	})
	if err != nil {
		t.Fatalf("open aggregator store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	hub := events.NewHub(64)
	svc := aggregator.NewService(store, hub, clock.System{}, idgen.UUID{}, 10000)
	server := &httpapi.Server{Service: svc, Hub: hub, MaxBodyBytes: 1 << 20, MaxFixesPerPacket: 10000, SnapshotMaxBytes: 16 << 20}
	ts := httptest.NewServer(server.Router())
	t.Cleanup(ts.Close)
	return ts
}

// postTelemetry sends one JSON telemetry packet directly to the aggregator,
// exactly as the real simulator would over HTTP (INS-05/06 explicitly
// permit driving aggregator state this way; gpsinspect itself never submits
// telemetry).
func postTelemetry(t *testing.T, baseURL, simulatorID, vehicleID, packetID string, fixes []map[string]any) {
	t.Helper()
	body := map[string]any{
		"simulatorId": simulatorID, "vehicleId": vehicleID, "packetId": packetID, "fixes": fixes,
	}
	data, _ := json.Marshal(body)
	resp, err := http.Post(baseURL+"/api/v1/telemetry", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("post telemetry: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("post telemetry: expected 200, got %d", resp.StatusCode)
	}
}

func fixJSON(tsMs int64, lat, lon, speed, heading float64) map[string]any {
	return map[string]any{
		"timestampMs": fmt.Sprintf("%d", tsMs), "latitude": lat, "longitude": lon,
		"speedMps": speed, "headingDegrees": heading,
	}
}

// TestI16_ListInspectOutageRecoveryValidateMergedForceClose drives the full
// INS-06/E2E-01/E2E-02 scenario against a real aggregator using only
// gpsinspect commands (plus direct telemetry POSTs standing in for the
// simulator): normal playback, an outage past 600s producing two trips, a
// historical merge, validating the merged state, an internal-gap forced
// close with warning, and a saved immutability baseline.
func TestI16_ListInspectOutageRecoveryValidateMergedForceClose(t *testing.T) {
	srv := newRealAggregator(t)
	vehicleID := "vehicle-e2e"
	simID := "sim-e2e"

	// Phase 1: normal playback, two fixes one second apart.
	postTelemetry(t, srv.URL, simID, vehicleID, "20261024-000001", []map[string]any{
		fixJSON(1000000, 40.0, -73.0, 1, 10),
		fixJSON(1001000, 40.001, -73.001, 1, 10),
	})

	// gpsinspect vehicles: the vehicle must now be listed.
	stdout, _, exit := runCLI(t, "", "--base-url", srv.URL, "vehicles")
	if exit != 0 || !strings.Contains(stdout, vehicleID) {
		t.Fatalf("expected vehicle listed, got exit=%d stdout=%s", exit, stdout)
	}

	// Phase 2: an outage of more than 600 simulated seconds, then resume
	// (simulated here by simply sending a second packet far enough in event
	// time ahead -- exactly the aggregator-observable effect of a real
	// outage/resume cycle, since gpsinspect only ever sees aggregator
	// state).
	postTelemetry(t, srv.URL, simID, vehicleID, "20261024-000002", []map[string]any{
		fixJSON(1700000, 40.01, -73.01, 1, 10), // 699000ms later: > 600s gap
	})

	stdout, _, exit = runCLI(t, "", "--base-url", srv.URL, "--json", "trips", vehicleID)
	if exit != 0 {
		t.Fatalf("expected exit 0 listing trips, got %d: %s", exit, stdout)
	}
	var tripsDoc struct {
		Data struct {
			Items []struct {
				TripID string `json:"trip_id"`
				Status string `json:"status"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &tripsDoc); err != nil {
		t.Fatalf("decode trips JSON: %v: %s", err, stdout)
	}
	if len(tripsDoc.Data.Items) != 2 {
		t.Fatalf("expected 2 current trips after the outage, got %d: %s", len(tripsDoc.Data.Items), stdout)
	}

	// Phase 3: bridge the gap with a historical (in event time) fix that
	// connects both trips (within 600000ms of trip1's end at 1001000 and
	// trip2's start at 1700000), producing a merge.
	postTelemetry(t, srv.URL, simID, vehicleID, "20261024-000003", []map[string]any{
		fixJSON(1300000, 40.0015, -73.0015, 1, 10),
	})

	stdout, _, exit = runCLI(t, "", "--base-url", srv.URL, "--json", "trips", vehicleID, "--include-merged")
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d: %s", exit, stdout)
	}
	if !strings.Contains(stdout, `"close_reason":"MERGED"`) {
		t.Fatalf("expected MERGED predecessors after the bridge, got: %s", stdout)
	}

	// gpsinspect validate must pass cleanly on the merged state.
	stdout, stderr, exit := runCLI(t, "", "--base-url", srv.URL, "--json", "validate", vehicleID)
	if exit != 0 {
		t.Fatalf("expected validate exit 0 on merged state, got %d: %s / %s", exit, stdout, stderr)
	}
	var validateDoc struct {
		Data struct {
			FailCount int    `json:"fail_count"`
			Overall   string `json:"overall"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &validateDoc); err != nil {
		t.Fatalf("decode validate JSON: %v: %s", err, stdout)
	}
	if validateDoc.Data.FailCount != 0 {
		t.Fatalf("expected 0 FAIL checks on merged state, got %d: %s", validateDoc.Data.FailCount, stdout)
	}

	// Phase 4: an internal gap on a NEW vehicle, forced close with warning.
	vehicle2 := "vehicle-e2e-gaps"
	postTelemetry(t, srv.URL, simID, vehicle2, "20261024-100001", []map[string]any{
		fixJSON(2000000, 41.0, -74.0, 1, 10),
		fixJSON(2011000, 41.001, -74.001, 1, 10), // 11s gap -> 10 missing fixes
	})

	// Find the trip ID.
	stdout, _, exit = runCLI(t, "", "--base-url", srv.URL, "--json", "trips", vehicle2)
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d: %s", exit, stdout)
	}
	var tripsDoc2 struct {
		Data struct {
			Items []struct {
				TripID string `json:"trip_id"`
			} `json:"items"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(stdout), &tripsDoc2)
	if len(tripsDoc2.Data.Items) != 1 {
		t.Fatalf("expected exactly 1 trip, got %d", len(tripsDoc2.Data.Items))
	}
	gapTripID := tripsDoc2.Data.Items[0].TripID

	// Ordinary close without --force must be rejected (confirmation
	// required), noninteractive.
	_, stderr, exit = runCLI(t, "", "--base-url", srv.URL, "close", gapTripID)
	if exit != 4 {
		t.Fatalf("expected exit 4 (confirmation required) for incomplete trip, got %d: %s", exit, stderr)
	}

	// Explicit --force closes it.
	stdout, stderr, exit = runCLI(t, "", "--base-url", srv.URL, "close", gapTripID, "--force")
	if exit != 0 {
		t.Fatalf("expected exit 0 with --force, got %d: %s / %s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "CLOSED") {
		t.Fatalf("expected CLOSED outcome, got: %s", stdout)
	}

	// validate must still pass (quarantine/closure handled correctly) and a
	// baseline can be saved from this known-good state.
	baselinePath := filepath.Join(t.TempDir(), "e2e-baseline.json")
	stdout, stderr, exit = runCLI(t, "", "--base-url", srv.URL, "validate", vehicle2, "--write-baseline", baselinePath)
	if exit != 0 {
		t.Fatalf("expected validate exit 0 after forced close, got %d: %s / %s", exit, stdout, stderr)
	}
	if _, err := os.Stat(baselinePath); err != nil {
		t.Fatalf("expected baseline file to be written: %v", err)
	}

	// A second validate run against the saved baseline must also pass,
	// including V09/V10 (nothing has changed).
	stdout, stderr, exit = runCLI(t, "", "--base-url", srv.URL, "validate", vehicle2, "--baseline", baselinePath)
	if exit != 0 {
		t.Fatalf("expected validate exit 0 against saved baseline, got %d: %s / %s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "V09") || !strings.Contains(stdout, "V10") {
		t.Fatalf("expected V09/V10 to run against the baseline: %s", stdout)
	}
}
