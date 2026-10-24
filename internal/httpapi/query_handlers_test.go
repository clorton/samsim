package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func postPacket(t *testing.T, ts string, baseURL string, packetID string, offsets ...int64) map[string]any {
	t.Helper()
	var fixes []string
	for _, off := range offsets {
		fixes = append(fixes, fmt.Sprintf(`{"timestampMs":"%d","latitude":44.0,"longitude":-121.0,"speedMps":1,"headingDegrees":0}`, baseMs+off))
	}
	body := fmt.Sprintf(`{"simulatorId":"%s","vehicleId":"vehicle-001","packetId":"%s","fixes":[%s]}`, ts, packetID, joinStrings(fixes, ","))
	resp, err := http.Post(baseURL+"/api/v1/telemetry", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest failed: %d: %s", resp.StatusCode, respBody)
	}
	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func joinStrings(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("invalid JSON response: %s: %v", body, err)
		}
	}
	return resp.StatusCode, result
}

func TestHandleListVehicles(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0)

	status, result := getJSON(t, ts.URL+"/api/v1/vehicles")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	items := result["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 vehicle, got %d", len(items))
	}
}

func TestHandleGetVehicle_NotFound(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	status, result := getJSON(t, ts.URL+"/api/v1/vehicles/nonexistent")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
	errObj := result["error"].(map[string]any)
	if errObj["code"] != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND, got %+v", errObj)
	}
}

func TestHandleGetSnapshot(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0, 1000)

	status, result := getJSON(t, ts.URL+"/api/v1/vehicles/vehicle-001/snapshot")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %+v", status, result)
	}
	trips := result["trips"].([]any)
	if len(trips) != 1 {
		t.Fatalf("expected 1 trip in snapshot, got %d", len(trips))
	}
}

func TestHandleListTrips(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0)

	status, result := getJSON(t, ts.URL+"/api/v1/vehicles/vehicle-001/trips")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	items := result["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 trip, got %d", len(items))
	}
}

func TestHandleListVehicleFixes(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0, 1000)

	status, result := getJSON(t, ts.URL+"/api/v1/vehicles/vehicle-001/fixes")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	items := result["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 fixes, got %d", len(items))
	}
}

func TestHandleGetTrip(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0)

	_, snap := getJSON(t, ts.URL+"/api/v1/vehicles/vehicle-001/snapshot")
	trips := snap["trips"].([]any)
	tripID := trips[0].(map[string]any)["trip_id"].(string)

	status, result := getJSON(t, ts.URL+"/api/v1/trips/"+tripID)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if result["trip_id"] != tripID {
		t.Fatalf("unexpected trip detail: %+v", result)
	}
}

func TestHandleGetTripFixes(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0, 1000)

	_, snap := getJSON(t, ts.URL+"/api/v1/vehicles/vehicle-001/snapshot")
	trips := snap["trips"].([]any)
	tripID := trips[0].(map[string]any)["trip_id"].(string)

	status, result := getJSON(t, ts.URL+"/api/v1/trips/"+tripID+"/fixes")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	items := result["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 fixes, got %d", len(items))
	}
}

func TestHandleGetCompleteness(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0, 11000) // large gap -> missing fixes

	_, snap := getJSON(t, ts.URL+"/api/v1/vehicles/vehicle-001/snapshot")
	trips := snap["trips"].([]any)
	tripID := trips[0].(map[string]any)["trip_id"].(string)

	status, result := getJSON(t, ts.URL+"/api/v1/trips/"+tripID+"/completeness")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if result["complete"] != false {
		t.Fatalf("expected incomplete, got %+v", result)
	}
	if result["missing_fix_count"].(float64) != 10 {
		t.Fatalf("expected 10 missing fixes, got %+v", result)
	}
}

func TestHandleCloseTrip_Success(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	ingestResult := postPacket(t, "sim-1", ts.URL, "20261024-000001", 0)
	revision := ingestResult["revision"].(float64)

	_, snap := getJSON(t, ts.URL+"/api/v1/vehicles/vehicle-001/snapshot")
	trips := snap["trips"].([]any)
	tripID := trips[0].(map[string]any)["trip_id"].(string)

	body := fmt.Sprintf(`{"expected_revision":%d,"force":false}`, int64(revision))
	resp, err := http.Post(ts.URL+"/api/v1/trips/"+tripID+"/close", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}
	var result map[string]any
	json.Unmarshal(respBody, &result)
	if result["status"] != "CLOSED" {
		t.Fatalf("expected CLOSED, got %+v", result)
	}
}

func TestHandleCloseTrip_MissingExpectedRevision(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0)

	_, snap := getJSON(t, ts.URL+"/api/v1/vehicles/vehicle-001/snapshot")
	trips := snap["trips"].([]any)
	tripID := trips[0].(map[string]any)["trip_id"].(string)

	resp, err := http.Post(ts.URL+"/api/v1/trips/"+tripID+"/close", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleHealth(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	status, result := getJSON(t, ts.URL+"/api/v1/health")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if result["healthy"] != true {
		t.Fatalf("expected healthy=true, got %+v", result)
	}
}
