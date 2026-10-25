package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
)

// fixtureServer is a minimal hand-built httptest server implementing just
// enough of AG-07's surface for gpsinspect command tests, independent of
// the real aggregator package (INS-06: "Use httptest fixtures").
type fixtureServer struct {
	mu sync.Mutex

	databaseID string
	vehicles   map[string]*fixtureVehicle
	trips      map[string]*fixtureTrip

	closeHandler func(tripID string, body map[string]any) (int, map[string]any)

	srv *httptest.Server
}

type fixtureVehicle struct {
	VehicleID             string
	ActiveTripID          *string
	Revision              int64
	LatestFix             map[string]any
	TripGapSeconds        int64
	ExpectedFixIntervalMs int64
}

type fixtureTrip struct {
	Summary      map[string]any
	Detail       map[string]any
	Geometry     []map[string]any
	Completeness map[string]any
}

func newFixtureServer() *fixtureServer {
	fs := &fixtureServer{
		databaseID: "db-fixture-1",
		vehicles:   map[string]*fixtureVehicle{},
		trips:      map[string]*fixtureTrip{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vehicles", fs.handleListVehicles)
	mux.HandleFunc("/api/v1/vehicles/{id}", fs.handleGetVehicle)
	mux.HandleFunc("/api/v1/vehicles/{id}/snapshot", fs.handleSnapshot)
	mux.HandleFunc("/api/v1/vehicles/{id}/trips", fs.handleListTrips)
	mux.HandleFunc("/api/v1/vehicles/{id}/fixes", fs.handleListFixes)
	mux.HandleFunc("/api/v1/trips/{id}", fs.handleGetTrip)
	mux.HandleFunc("/api/v1/trips/{id}/fixes", fs.handleGetTripFixes)
	mux.HandleFunc("/api/v1/trips/{id}/completeness", fs.handleGetCompleteness)
	mux.HandleFunc("/api/v1/trips/{id}/close", fs.handleClose)
	fs.srv = httptest.NewServer(mux)
	return fs
}

func (fs *fixtureServer) URL() string { return fs.srv.URL }
func (fs *fixtureServer) Close()      { fs.srv.Close() }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func errEnvelope(code, message string, details map[string]any) map[string]any {
	if details == nil {
		details = map[string]any{}
	}
	return map[string]any{"error": map[string]any{"code": code, "message": message, "details": details}}
}

func (fs *fixtureServer) handleListVehicles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, errEnvelope("METHOD_NOT_ALLOWED", "no", nil))
		return
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	var ids []string
	for id := range fs.vehicles {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	start := 0
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		for i, id := range ids {
			if id == cursor {
				start = i + 1
				break
			}
		}
	}

	end := start + limit
	if end > len(ids) {
		end = len(ids)
	}
	var items []map[string]any
	for _, id := range ids[start:end] {
		v := fs.vehicles[id]
		items = append(items, map[string]any{"vehicle_id": v.VehicleID, "active_trip_id": v.ActiveTripID, "revision": v.Revision})
	}
	var nextCursor *string
	if end < len(ids) {
		c := ids[end-1]
		nextCursor = &c
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nextCursor, "database_id": fs.databaseID})
}

func (fs *fixtureServer) handleGetVehicle(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	v, ok := fs.vehicles[r.PathValue("id")]
	if !ok {
		writeJSON(w, 404, errEnvelope("NOT_FOUND", "vehicle not found", nil))
		return
	}
	writeJSON(w, 200, map[string]any{
		"vehicle_id": v.VehicleID, "latest_fix": v.LatestFix, "active_trip_id": v.ActiveTripID,
		"revision": v.Revision, "database_id": fs.databaseID,
		"trip_gap_seconds": v.TripGapSeconds, "expected_fix_interval_ms": v.ExpectedFixIntervalMs,
	})
}

func (fs *fixtureServer) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	v, ok := fs.vehicles[r.PathValue("id")]
	if !ok {
		writeJSON(w, 404, errEnvelope("NOT_FOUND", "vehicle not found", nil))
		return
	}
	var trips []map[string]any
	for _, t := range fs.trips {
		if t.Summary["vehicle_id"] == v.VehicleID && t.Summary["close_reason"] != "MERGED" {
			trips = append(trips, t.Summary)
		}
	}
	writeJSON(w, 200, map[string]any{
		"vehicle": map[string]any{
			"vehicle_id": v.VehicleID, "latest_fix": v.LatestFix, "active_trip_id": v.ActiveTripID,
			"revision": v.Revision, "database_id": fs.databaseID,
			"trip_gap_seconds": v.TripGapSeconds, "expected_fix_interval_ms": v.ExpectedFixIntervalMs,
		},
		"trips": trips, "manual_barriers": []map[string]any{}, "revision": v.Revision, "database_id": fs.databaseID,
	})
}

func (fs *fixtureServer) handleListTrips(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	vehicleID := r.PathValue("id")
	v, ok := fs.vehicles[vehicleID]
	if !ok {
		writeJSON(w, 404, errEnvelope("NOT_FOUND", "vehicle not found", nil))
		return
	}
	if atRev := r.URL.Query().Get("at_revision"); atRev != "" {
		if atRev != fmt.Sprintf("%d", v.Revision) {
			writeJSON(w, 409, errEnvelope("REVISION_CONFLICT", "stale", map[string]any{"revision": v.Revision}))
			return
		}
	}
	includeMerged := r.URL.Query().Get("include_merged") == "true"
	status := r.URL.Query().Get("status")
	var items []map[string]any
	for _, t := range fs.trips {
		if t.Summary["vehicle_id"] != vehicleID {
			continue
		}
		if !includeMerged && t.Summary["close_reason"] == "MERGED" {
			continue
		}
		if status != "" && t.Summary["status"] != status {
			continue
		}
		items = append(items, t.Summary)
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nil, "revision": v.Revision, "database_id": fs.databaseID})
}

func (fs *fixtureServer) handleListFixes(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	vehicleID := r.PathValue("id")
	v, ok := fs.vehicles[vehicleID]
	if !ok {
		writeJSON(w, 404, errEnvelope("NOT_FOUND", "vehicle not found", nil))
		return
	}
	var items []map[string]any
	for _, t := range fs.trips {
		if t.Summary["vehicle_id"] != vehicleID {
			continue
		}
		for _, g := range t.Geometry {
			items = append(items, g)
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nil, "revision": v.Revision, "database_id": fs.databaseID})
}

func (fs *fixtureServer) handleGetTrip(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	t, ok := fs.trips[r.PathValue("id")]
	if !ok {
		writeJSON(w, 404, errEnvelope("NOT_FOUND", "trip not found", nil))
		return
	}
	if atRev := r.URL.Query().Get("at_revision"); atRev != "" {
		vehicleID := t.Summary["vehicle_id"].(string)
		v := fs.vehicles[vehicleID]
		if atRev != fmt.Sprintf("%d", v.Revision) {
			writeJSON(w, 409, errEnvelope("REVISION_CONFLICT", "stale", map[string]any{"revision": v.Revision}))
			return
		}
	}
	writeJSON(w, 200, t.Detail)
}

func (fs *fixtureServer) handleGetTripFixes(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	t, ok := fs.trips[r.PathValue("id")]
	if !ok {
		writeJSON(w, 404, errEnvelope("NOT_FOUND", "trip not found", nil))
		return
	}
	vehicleID := t.Summary["vehicle_id"].(string)
	v := fs.vehicles[vehicleID]
	writeJSON(w, 200, map[string]any{"items": t.Geometry, "next_cursor": nil, "revision": v.Revision, "database_id": fs.databaseID})
}

func (fs *fixtureServer) handleGetCompleteness(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	t, ok := fs.trips[r.PathValue("id")]
	if !ok {
		writeJSON(w, 404, errEnvelope("NOT_FOUND", "trip not found", nil))
		return
	}
	writeJSON(w, 200, t.Completeness)
}

func (fs *fixtureServer) handleClose(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	fs.mu.Lock()
	handler := fs.closeHandler
	tripID := r.PathValue("id")
	fs.mu.Unlock()
	if handler == nil {
		writeJSON(w, 500, errEnvelope("INTERNAL", "no close handler configured", nil))
		return
	}
	status, resp := handler(tripID, body)
	writeJSON(w, status, resp)
}
