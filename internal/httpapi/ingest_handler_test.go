package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"google.golang.org/protobuf/proto"

	telemetryv1 "gpsdemo/gen/telemetry/v1"
)

func validJSONPacket(packetID string, timestampOffset int64) []byte {
	return []byte(fmt.Sprintf(`{
		"simulatorId": "sim-001",
		"vehicleId": "vehicle-001",
		"packetId": "%s",
		"fixes": [
			{"timestampMs":"%d","latitude":44.0582,"longitude":-121.3153,"speedMps":5,"headingDegrees":90}
		]
	}`, packetID, baseMs+timestampOffset))
}

func TestHandleIngest_JSON_FirstAcceptance(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)

	resp, err := http.Post(ts.URL+"/api/v1/telemetry", "application/json", bytes.NewReader(validJSONPacket("20261024-000001", 0)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result["duplicate_packet"] != false {
		t.Fatalf("expected duplicate_packet=false, got %+v", result)
	}
	if result["accepted_fixes"].(float64) != 1 {
		t.Fatalf("expected accepted_fixes=1, got %+v", result)
	}
}

func TestHandleIngest_Protobuf(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)

	pkt := &telemetryv1.TelemetryPacket{
		SimulatorId: "sim-001",
		VehicleId:   "vehicle-001",
		PacketId:    "20261024-000001",
		Fixes: []*telemetryv1.GPSFix{
			{TimestampMs: baseMs, Latitude: 44.0582, Longitude: -121.3153, SpeedMps: 5, HeadingDegrees: 90},
		},
	}
	data, err := proto.Marshal(pkt)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Post(ts.URL+"/api/v1/telemetry", "application/x-protobuf", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestHandleIngest_UnsupportedMediaType(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)

	resp, err := http.Post(ts.URL+"/api/v1/telemetry", "text/plain", bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", resp.StatusCode)
	}
	assertErrorEnvelope(t, resp, "UNSUPPORTED_MEDIA_TYPE")
}

func TestHandleIngest_MalformedJSON(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)

	resp, err := http.Post(ts.URL+"/api/v1/telemetry", "application/json", bytes.NewReader([]byte("{not json")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleIngest_BodyTooLarge(t *testing.T) {
	s, _ := newTestServer(t)
	s.MaxBodyBytes = 10 // tiny limit
	ts := httptestServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/telemetry", "application/json", bytes.NewReader(validJSONPacket("20261024-000001", 0)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}

func TestHandleIngest_WrongMethod(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)

	resp, err := http.Get(ts.URL + "/api/v1/telemetry")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func assertErrorEnvelope(t *testing.T, resp *http.Response, wantCode string) {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("expected error envelope, got %s: %v", body, err)
	}
	if env.Error.Code != wantCode {
		t.Fatalf("expected code %s, got %s", wantCode, env.Error.Code)
	}
}
