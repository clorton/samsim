package main

// seedSimpleVehicle installs a vehicle with one ACTIVE trip containing two
// fixes, returning the trip ID, for tests that just need a minimal working
// fixture.
func (fs *fixtureServer) seedSimpleVehicle(vehicleID string) (tripID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	tripID = "trip-" + vehicleID + "-1"
	geometry := []map[string]any{
		{"timestamp_ms": int64(1000), "latitude": 40.0, "longitude": -73.0, "speed_mps": 1.0, "heading_degrees": 10.0, "current_trip_id": tripID, "assignment_reason": nil},
		{"timestamp_ms": int64(2000), "latitude": 40.001, "longitude": -73.001, "speed_mps": 1.5, "heading_degrees": 11.0, "current_trip_id": tripID, "assignment_reason": nil},
	}
	summary := map[string]any{
		"trip_id": tripID, "vehicle_id": vehicleID, "start_ms": int64(1000), "end_ms": int64(2000),
		"fix_count": int64(2),
		"bounds":    map[string]any{"min_latitude": 40.0, "max_latitude": 40.001, "min_longitude": -73.001, "max_longitude": -73.0},
		"status":    "ACTIVE", "close_reason": "NONE", "superseded_by_trip_id": nil,
		"created_revision": int64(1), "updated_revision": int64(1), "closed_at_ms": nil, "forced_close": false,
	}
	detail := map[string]any{}
	for k, v := range summary {
		detail[k] = v
	}
	detail["immediate_predecessor_trip_ids"] = []string{}
	detail["immediate_successor_trip_id"] = nil
	detail["terminal_successor_trip_id"] = nil
	detail["completeness"] = map[string]any{
		"trip_id": tripID, "revision": int64(1), "expected_fix_interval_ms": int64(1000),
		"missing_fix_count": int64(0), "missing_intervals": nil, "complete": true,
		"boundary_completeness": "UNKNOWN", "assessment": "NO_INTERNAL_GAPS",
	}

	fs.trips[tripID] = &fixtureTrip{
		Summary: summary, Detail: detail, Geometry: geometry,
		Completeness: detail["completeness"].(map[string]any),
	}

	active := tripID
	fs.vehicles[vehicleID] = &fixtureVehicle{
		VehicleID: vehicleID, ActiveTripID: &active, Revision: 1,
		LatestFix:             geometry[1],
		TripGapSeconds:        600,
		ExpectedFixIntervalMs: 1000,
	}
	return tripID
}

// seedVehicleWithGaps installs a vehicle with one ACTIVE trip that has an
// internal completeness gap (two fixes 11 seconds apart at a 1000ms
// expected interval -> 10 missing).
func (fs *fixtureServer) seedVehicleWithGaps(vehicleID string) (tripID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	tripID = "trip-" + vehicleID + "-gaps"
	geometry := []map[string]any{
		{"timestamp_ms": int64(1000), "latitude": 40.0, "longitude": -73.0, "speed_mps": 1.0, "heading_degrees": 10.0, "current_trip_id": tripID, "assignment_reason": nil},
		{"timestamp_ms": int64(12000), "latitude": 40.001, "longitude": -73.001, "speed_mps": 1.5, "heading_degrees": 11.0, "current_trip_id": tripID, "assignment_reason": nil},
	}
	summary := map[string]any{
		"trip_id": tripID, "vehicle_id": vehicleID, "start_ms": int64(1000), "end_ms": int64(12000),
		"fix_count": int64(2),
		"bounds":    map[string]any{"min_latitude": 40.0, "max_latitude": 40.001, "min_longitude": -73.001, "max_longitude": -73.0},
		"status":    "ACTIVE", "close_reason": "NONE", "superseded_by_trip_id": nil,
		"created_revision": int64(1), "updated_revision": int64(1), "closed_at_ms": nil, "forced_close": false,
	}
	detail := map[string]any{}
	for k, v := range summary {
		detail[k] = v
	}
	completeness := map[string]any{
		"trip_id": tripID, "revision": int64(1), "expected_fix_interval_ms": int64(1000),
		"missing_fix_count": int64(10),
		"missing_intervals": []map[string]any{
			{"start_ms": int64(2000), "end_ms": int64(11000), "missing_count": int64(10)},
		},
		"complete": false, "boundary_completeness": "UNKNOWN", "assessment": "INTERNAL_GAPS",
	}
	detail["immediate_predecessor_trip_ids"] = []string{}
	detail["immediate_successor_trip_id"] = nil
	detail["terminal_successor_trip_id"] = nil
	detail["completeness"] = completeness

	fs.trips[tripID] = &fixtureTrip{Summary: summary, Detail: detail, Geometry: geometry, Completeness: completeness}

	active := tripID
	fs.vehicles[vehicleID] = &fixtureVehicle{
		VehicleID: vehicleID, ActiveTripID: &active, Revision: 1,
		LatestFix: geometry[1], TripGapSeconds: 600, ExpectedFixIntervalMs: 1000,
	}
	return tripID
}
