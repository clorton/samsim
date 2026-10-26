package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSimulatorActionForwardsConfirmationBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/reset" || string(b) != `{"discard_pending":true}` {
			t.Errorf("lost confirmation: %s %s", r.URL.Path, b)
		}
		w.Write([]byte(`{"status":{"playback_state":"READY"}}`))
	}))
	defer upstream.Close()
	ts := newTestServerWithControl(t, upstream.URL)
	response, err := http.Post(ts.URL+"/api/v1/simulators/sim-1/reset", "application/json", strings.NewReader(`{"discard_pending":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("got %d", response.StatusCode)
	}
}

func TestSimulatorUploadUsesCSVAndFixedUpstreamPath(t *testing.T) {
	csv := "ts,lat,lon,speed_mps,heading_deg\n0,40,-73,0,0\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/routes" || r.Header.Get("Content-Type") != "text/csv" || string(b) != csv {
			t.Errorf("bad proxy request: %s %s", r.URL.Path, b)
		}
		w.Write([]byte(`{"route_id":"upload-test","fix_count":1}`))
	}))
	defer upstream.Close()
	ts := newTestServerWithControl(t, upstream.URL)
	response, err := http.Post(ts.URL+"/api/v1/simulators/sim-1/routes", "text/csv", strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("got %d", response.StatusCode)
	}
}
