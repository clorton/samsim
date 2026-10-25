package simulator

import (
	"encoding/json"
	"strings"
	"testing"
)

func validConfigJSON() string {
	return `{
		"listen":"127.0.0.1:8090",
		"simulator_id":"sim-001",
		"vehicle_id":"vehicle-001",
		"aggregator_url":"http://127.0.0.1:8080",
		"routes":{"demo":"./routes/fixes.csv"},
		"route_id":"demo",
		"run_start_ms":"now",
		"clock_multiplier":100,
		"expected_fix_interval_ms":1000,
		"packet_size":10,
		"encoding":"json",
		"max_packets_per_second":50,
		"live_weight":3,
		"backlog_weight":1,
		"partial_flush_wall_ms":200,
		"request_timeout_ms":2000,
		"identity_db_path":"./data/simulator-identity.sqlite",
		"max_route_bytes":67108864,
		"max_route_rows":1000000
	}`
}

func TestParseConfig_ValidExample(t *testing.T) {
	cfg, err := ParseConfig([]byte(validConfigJSON()))
	if err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
	if cfg.SimulatorID != "sim-001" || cfg.VehicleID != "vehicle-001" {
		t.Fatalf("unexpected IDs: %+v", cfg)
	}
	if !cfg.RunStartMs.IsNow {
		t.Fatalf("expected run_start_ms=now")
	}
	if cfg.ClockMultiplier != 100 {
		t.Fatalf("unexpected clock_multiplier: %v", cfg.ClockMultiplier)
	}
}

func TestParseConfig_FixedRunStart(t *testing.T) {
	raw := strings.Replace(validConfigJSON(), `"run_start_ms":"now"`, `"run_start_ms":1792845600000`, 1)
	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
	if cfg.RunStartMs.IsNow || cfg.RunStartMs.FixedMs != 1792845600000 {
		t.Fatalf("unexpected run_start_ms: %+v", cfg.RunStartMs)
	}
}

func TestParseConfig_RunStartMarshalRoundTrip(t *testing.T) {
	now := RunStart{IsNow: true}
	data, err := json.Marshal(now)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"now"` {
		t.Fatalf("expected quoted now, got %s", data)
	}

	fixed := RunStart{FixedMs: 123}
	data, err = json.Marshal(fixed)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "123" {
		t.Fatalf("expected numeric 123, got %s", data)
	}
}

func TestParseConfig_RejectsUnknownFields(t *testing.T) {
	raw := strings.Replace(validConfigJSON(), `"packet_size":10,`, `"packet_size":10,"bogus_field":1,`, 1)
	if _, err := ParseConfig([]byte(raw)); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestParseConfig_InvalidFields(t *testing.T) {
	cases := []struct {
		name    string
		replace string
		with    string
	}{
		{"bad simulator_id", `"simulator_id":"sim-001"`, `"simulator_id":"bad id!"`},
		{"bad vehicle_id", `"vehicle_id":"vehicle-001"`, `"vehicle_id":""`},
		{"bad aggregator_url scheme", `"aggregator_url":"http://127.0.0.1:8080"`, `"aggregator_url":"ftp://127.0.0.1:8080"`},
		{"aggregator_url with path", `"aggregator_url":"http://127.0.0.1:8080"`, `"aggregator_url":"http://127.0.0.1:8080/foo"`},
		{"aggregator_url with query", `"aggregator_url":"http://127.0.0.1:8080"`, `"aggregator_url":"http://127.0.0.1:8080?x=1"`},
		{"aggregator_url with creds", `"aggregator_url":"http://127.0.0.1:8080"`, `"aggregator_url":"http://user:pass@127.0.0.1:8080"`},
		{"unknown route_id", `"route_id":"demo"`, `"route_id":"nonexistent"`},
		{"multiplier too low", `"clock_multiplier":100`, `"clock_multiplier":5`},
		{"multiplier too high", `"clock_multiplier":100`, `"clock_multiplier":5000`},
		{"packet_size zero", `"packet_size":10`, `"packet_size":0`},
		{"packet_size too big", `"packet_size":10`, `"packet_size":100000`},
		{"bad encoding", `"encoding":"json"`, `"encoding":"xml"`},
		{"rate zero", `"max_packets_per_second":50`, `"max_packets_per_second":0`},
		{"rate too big", `"max_packets_per_second":50`, `"max_packets_per_second":99999`},
		{"live_weight zero", `"live_weight":3`, `"live_weight":0`},
		{"backlog_weight too big", `"backlog_weight":1`, `"backlog_weight":1000`},
		{"partial_flush zero", `"partial_flush_wall_ms":200`, `"partial_flush_wall_ms":0`},
		{"request_timeout zero", `"request_timeout_ms":2000`, `"request_timeout_ms":0`},
		{"max_route_bytes zero", `"max_route_bytes":67108864`, `"max_route_bytes":0`},
		{"max_route_rows zero", `"max_route_rows":1000000`, `"max_route_rows":0`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := strings.Replace(validConfigJSON(), c.replace, c.with, 1)
			if raw == validConfigJSON() {
				t.Fatalf("test setup error: replacement %q not found in base JSON", c.replace)
			}
			if _, err := ParseConfig([]byte(raw)); err == nil {
				t.Fatalf("expected validation error for %s", c.name)
			}
		})
	}
}

func TestParseConfig_EmptyRoutes(t *testing.T) {
	raw := strings.Replace(validConfigJSON(), `"routes":{"demo":"./routes/fixes.csv"},`, `"routes":{},`, 1)
	if _, err := ParseConfig([]byte(raw)); err == nil {
		t.Fatal("expected error for empty routes map")
	}
}

func TestValidateAggregatorURL_AppendsTelemetryPath(t *testing.T) {
	full, err := ValidateAggregatorURL("http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if full != "http://127.0.0.1:8080/api/v1/telemetry" {
		t.Fatalf("unexpected URL: %s", full)
	}
}

func TestValidateAggregatorURL_TrailingSlashAllowed(t *testing.T) {
	full, err := ValidateAggregatorURL("http://127.0.0.1:8080/")
	if err != nil {
		t.Fatal(err)
	}
	if full != "http://127.0.0.1:8080/api/v1/telemetry" {
		t.Fatalf("unexpected URL: %s", full)
	}
}

func TestValidateAggregatorURL_RejectsFragment(t *testing.T) {
	if _, err := ValidateAggregatorURL("http://127.0.0.1:8080#frag"); err == nil {
		t.Fatal("expected error for fragment")
	}
}
