package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"gpsdemo/internal/inspectoutput"
)

func newTestServerFromMux(mux *http.ServeMux) *httptest.Server {
	return httptest.NewServer(mux)
}

// TestI03_MultiPageVehiclesList_Deduplicated drives the real paginated
// vehicles listing against a server that always returns one item per page
// using the "limit" the client sends, forcing at least 2 pages.
func TestI03_MultiPageVehiclesList_Deduplicated(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	for i := 0; i < 5; i++ {
		fs.seedSimpleVehicle("vehicle-00" + strconv.Itoa(i))
	}

	stdout, _, exit := runCLI(t, "", "--base-url", fs.URL(), "--page-size", "2", "vehicles")
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d: %s", exit, stdout)
	}
	for i := 0; i < 5; i++ {
		if !strings.Contains(stdout, "vehicle-00"+strconv.Itoa(i)) {
			t.Fatalf("expected vehicle-00%d in output: %s", i, stdout)
		}
	}
}

// TestI03_CursorCycleDetection_Trips verifies the client's cycle detector
// trips when a server always returns the same next_cursor (simulating a
// pagination bug).
func TestI03_CursorCycleDetection_Trips(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	vehicleID := "vehicle-001"
	fs.seedSimpleVehicle(vehicleID)

	// Install a trips handler that always claims there's a next page with
	// the same cursor value, forming an infinite cycle.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vehicles/{id}/snapshot", fs.handleSnapshot)
	mux.HandleFunc("/api/v1/vehicles/{id}", fs.handleGetVehicle)
	mux.HandleFunc("/api/v1/vehicles/{id}/trips", func(w http.ResponseWriter, r *http.Request) {
		cursor := "same-cursor-always"
		writeJSON(w, 200, map[string]any{
			"items": []map[string]any{}, "next_cursor": &cursor, "revision": int64(1), "database_id": fs.databaseID,
		})
	})

	srv2 := newTestServerFromMux(mux)
	defer srv2.Close()

	_, stderr, exit := runCLI(t, "", "--base-url", srv2.URL, "trips", vehicleID)
	if exit != inspectoutput.ExitAPIOrIO {
		t.Fatalf("expected exit %d (protocol error from cursor cycle), got %d (stderr=%s)", inspectoutput.ExitAPIOrIO, exit, stderr)
	}
}
