package simulator

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBrowserUploadValidatesSelectsAndStarts(t *testing.T) {
	ts, e, _ := newTestHTTPServer(t)
	response, err := http.Post(ts.URL+"/routes", "text/csv", strings.NewReader(buildRouteCSV(12)))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("upload status %d", response.StatusCode)
	}
	var uploaded struct {
		RouteID  string `json:"route_id"`
		FixCount int    `json:"fix_count"`
	}
	if err := json.NewDecoder(response.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.FixCount != 12 || !strings.HasPrefix(uploaded.RouteID, "upload-") {
		t.Fatalf("bad metadata %+v", uploaded)
	}
	if err := e.PutConfig(ConfigUpdate{RouteID: &uploaded.RouteID}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if got := e.Status().RouteRows; got != 12 {
		t.Fatalf("started wrong CSV: %d rows", got)
	}
	response2, err := http.Post(ts.URL+"/routes", "text/csv", strings.NewReader(buildRouteCSV(15)))
	if err != nil {
		t.Fatal(err)
	}
	defer response2.Body.Close()
	if response2.StatusCode != 400 {
		t.Fatalf("running upload allowed: %d", response2.StatusCode)
	}
}

func TestBrowserUploadRejectsBadCSVWithoutMutation(t *testing.T) {
	ts, e, _ := newTestHTTPServer(t)
	before := e.Status().ControlRevision
	response, err := http.Post(ts.URL+"/routes", "text/csv", strings.NewReader("ts,lat\n0,91\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("got %d", response.StatusCode)
	}
	if e.Status().ControlRevision != before {
		t.Fatal("invalid upload changed configuration")
	}
}

func TestMultiplierOneSupported(t *testing.T) {
	cfg := Default()
	cfg.SimulatorID = "sim"
	cfg.VehicleID = "vehicle"
	cfg.Routes = map[string]string{"r": "route.csv"}
	cfg.RouteID = "r"
	cfg.ClockMultiplier = 1
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
