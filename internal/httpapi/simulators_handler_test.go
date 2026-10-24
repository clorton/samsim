package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gpsdemo/internal/control"
)

func newTestServerWithControl(t *testing.T, upstreamURL string) *httptest.Server {
	t.Helper()
	s, _ := newTestServer(t)
	s.Control = control.NewProxy([]control.Endpoint{
		{SimulatorID: "sim-1", ControlURL: upstreamURL, VehicleID: "vehicle-1"},
	})
	ts := httptestServer(s)
	t.Cleanup(ts.Close)
	return ts
}

func TestHandleListSimulators(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call upstream for list")
	}))
	defer upstream.Close()

	ts := newTestServerWithControl(t, upstream.URL)
	status, result := getJSON(t, ts.URL+"/api/v1/simulators")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	items := result["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 simulator, got %d", len(items))
	}
}

func TestHandleSimulatorStatus_UnknownID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	ts := newTestServerWithControl(t, upstream.URL)
	status, result := getJSON(t, ts.URL+"/api/v1/simulators/nonexistent/status")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
	errObj := result["error"].(map[string]any)
	if errObj["code"] != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND, got %+v", errObj)
	}
}

func TestHandleSimulatorStatus_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":"running"}`))
	}))
	defer upstream.Close()

	ts := newTestServerWithControl(t, upstream.URL)
	status, result := getJSON(t, ts.URL+"/api/v1/simulators/sim-1/status")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if result["state"] != "running" {
		t.Fatalf("unexpected status body: %+v", result)
	}
}

func TestHandleSimulatorAction_UnknownAction(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	ts := newTestServerWithControl(t, upstream.URL)
	resp, err := http.Post(ts.URL+"/api/v1/simulators/sim-1/bogus-action", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandleSimulatorStatus_Unavailable(t *testing.T) {
	ts := newTestServerWithControl(t, "http://127.0.0.1:1")
	status, result := getJSON(t, ts.URL+"/api/v1/simulators/sim-1/status")
	if status != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", status)
	}
	errObj := result["error"].(map[string]any)
	if errObj["code"] != "SIMULATOR_UNAVAILABLE" {
		t.Fatalf("expected SIMULATOR_UNAVAILABLE, got %+v", errObj)
	}
}
