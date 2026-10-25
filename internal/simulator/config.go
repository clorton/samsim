package simulator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
)

// RunStart represents the configured run_start_ms value, which is either
// the literal string "now" (resolve once, at first Start) or a fixed
// positive epoch-millisecond integer (SIM-01/02).
type RunStart struct {
	IsNow   bool
	FixedMs int64 // meaningful only when !IsNow
}

func (r RunStart) MarshalJSON() ([]byte, error) {
	if r.IsNow {
		return json.Marshal("now")
	}
	return json.Marshal(r.FixedMs)
}

func (r *RunStart) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		if asString != "now" {
			return fmt.Errorf("run_start_ms: string value must be \"now\", got %q", asString)
		}
		r.IsNow = true
		r.FixedMs = 0
		return nil
	}
	var asNumber int64
	if err := json.Unmarshal(data, &asNumber); err != nil {
		return fmt.Errorf("run_start_ms: must be the string \"now\" or a positive integer epoch millisecond value: %w", err)
	}
	if asNumber <= 0 {
		return fmt.Errorf("run_start_ms: fixed value must be a positive epoch millisecond, got %d", asNumber)
	}
	r.IsNow = false
	r.FixedMs = asNumber
	return nil
}

// Config is the simulator's complete startup configuration (SIM-01).
//
// Per the "separate normal reporting from transmission capacity" change
// request, NormalReportIntervalSimulatedMs controls WHEN live reports
// (packets) are created from newly generated fixes, while
// MaxPacketsPerSimulatedSecond controls the combined live+backlog send
// capacity. These are independent schedules: the first is a simulated-time
// clock (one report per window, regardless of transmission capacity); the
// second is a rate ceiling scaled by ClockMultiplier into an actual
// (wall-clock) packets/second limit. PacketSize remains only as a safety
// bound: if a closed report window contains more fixes than PacketSize, it
// is split into multiple bounded packets at the window's deadline, never
// deferred to a later window.
type Config struct {
	Listen                          string            `json:"listen"`
	SimulatorID                     string            `json:"simulator_id"`
	VehicleID                       string            `json:"vehicle_id"`
	AggregatorURL                   string            `json:"aggregator_url"`
	Routes                          map[string]string `json:"routes"`
	RouteID                         string            `json:"route_id"`
	RunStartMs                      RunStart          `json:"run_start_ms"`
	ClockMultiplier                 float64           `json:"clock_multiplier"`
	ExpectedFixIntervalMs           int64             `json:"expected_fix_interval_ms"`
	NormalReportIntervalSimulatedMs int64             `json:"normal_report_interval_simulated_ms"`
	PacketSize                      int               `json:"packet_size"`
	Encoding                        string            `json:"encoding"`
	MaxPacketsPerSimulatedSecond    float64           `json:"max_packets_per_simulated_second"`
	LiveWeight                      int               `json:"live_weight"`
	BacklogWeight                   int               `json:"backlog_weight"`
	RequestTimeoutMs                int64             `json:"request_timeout_ms"`
	IdentityDBPath                  string            `json:"identity_db_path"`
	MaxRouteBytes                   int64             `json:"max_route_bytes"`
	MaxRouteRows                    int64             `json:"max_route_rows"`
}

// Default returns the spec's example configuration values, useful as a
// starting point for tests and documentation; callers must still set
// simulator-specific fields (routes, identity_db_path, etc.) appropriately.
//
// MaxPacketsPerSimulatedSecond defaults to 0.2, deliberately just above the
// ~0.1 packets/simulated-second demand implied by the default 10-second
// report interval at a 1 Hz source cadence, so that a recovery backlog
// drain is slow enough to observe rather than instantaneous.
func Default() Config {
	return Config{
		Listen:                          "127.0.0.1:8090",
		AggregatorURL:                   "http://127.0.0.1:8080",
		RunStartMs:                      RunStart{IsNow: true},
		ClockMultiplier:                 100,
		ExpectedFixIntervalMs:           1000,
		NormalReportIntervalSimulatedMs: 10000,
		PacketSize:                      10,
		Encoding:                        "json",
		MaxPacketsPerSimulatedSecond:    0.2,
		LiveWeight:                      3,
		BacklogWeight:                   1,
		RequestTimeoutMs:                2000,
		IdentityDBPath:                  "./data/simulator-identity.sqlite",
		MaxRouteBytes:                   64 << 20,
		MaxRouteRows:                    1_000_000,
	}
}

// LoadConfig reads and validates a JSON config file.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// ParseConfig decodes and validates a JSON config document, applying
// Default() for any field not present in data.
func ParseConfig(data []byte) (Config, error) {
	cfg := Default()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// assumedAggregatorMaxBodyBytes and assumedMaxJSONBytesPerFix are a
// conservative sanity-check estimate used only to catch grossly
// misconfigured packet_size values before they would certainly exceed the
// aggregator's default max_body_bytes (1 MiB); this is an approximation,
// not an exact accounting of JSON/Protobuf overhead, since actual encoded
// size also depends on the simulator/vehicle/packet ID lengths and exact
// numeric formatting.
const (
	assumedAggregatorMaxBodyBytes = 1 << 20
	assumedMaxJSONBytesPerFix     = 160
)

// Validate checks all configuration fields per SIM-01's constraints.
func (c *Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("config: listen must not be empty")
	}
	if !idPattern.MatchString(c.SimulatorID) {
		return fmt.Errorf("config: simulator_id must match [A-Za-z0-9._-]{1,128}, got %q", c.SimulatorID)
	}
	if !idPattern.MatchString(c.VehicleID) {
		return fmt.Errorf("config: vehicle_id must match [A-Za-z0-9._-]{1,128}, got %q", c.VehicleID)
	}
	if _, err := ValidateAggregatorURL(c.AggregatorURL); err != nil {
		return fmt.Errorf("config: aggregator_url: %w", err)
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("config: routes map must not be empty")
	}
	for id, path := range c.Routes {
		if id == "" {
			return fmt.Errorf("config: routes: route ID must not be empty")
		}
		if path == "" {
			return fmt.Errorf("config: routes[%q]: path must not be empty", id)
		}
	}
	if _, ok := c.Routes[c.RouteID]; !ok {
		return fmt.Errorf("config: route_id %q is not present in routes map", c.RouteID)
	}
	if !c.RunStartMs.IsNow && c.RunStartMs.FixedMs <= 0 {
		return fmt.Errorf("config: run_start_ms fixed value must be positive")
	}
	if math.IsNaN(c.ClockMultiplier) || math.IsInf(c.ClockMultiplier, 0) || c.ClockMultiplier < 10 || c.ClockMultiplier > 1000 {
		return fmt.Errorf("config: clock_multiplier must be finite in [10,1000], got %v", c.ClockMultiplier)
	}
	if c.ExpectedFixIntervalMs <= 0 {
		return fmt.Errorf("config: expected_fix_interval_ms must be positive, got %d", c.ExpectedFixIntervalMs)
	}
	if c.NormalReportIntervalSimulatedMs <= 0 {
		return fmt.Errorf("config: normal_report_interval_simulated_ms must be positive, got %d", c.NormalReportIntervalSimulatedMs)
	}
	if c.PacketSize < 1 || c.PacketSize > 10000 {
		return fmt.Errorf("config: packet_size must be an integer in [1,10000], got %d", c.PacketSize)
	}
	if c.Encoding != "json" && c.Encoding != "protobuf" {
		return fmt.Errorf("config: encoding must be \"json\" or \"protobuf\", got %q", c.Encoding)
	}
	if math.IsNaN(c.MaxPacketsPerSimulatedSecond) || math.IsInf(c.MaxPacketsPerSimulatedSecond, 0) || c.MaxPacketsPerSimulatedSecond <= 0 || c.MaxPacketsPerSimulatedSecond > 10000 {
		return fmt.Errorf("config: max_packets_per_simulated_second must be finite in (0,10000], got %v", c.MaxPacketsPerSimulatedSecond)
	}
	if c.LiveWeight < 1 || c.LiveWeight > 100 {
		return fmt.Errorf("config: live_weight must be an integer in [1,100], got %d", c.LiveWeight)
	}
	if c.BacklogWeight < 1 || c.BacklogWeight > 100 {
		return fmt.Errorf("config: backlog_weight must be an integer in [1,100], got %d", c.BacklogWeight)
	}
	if c.RequestTimeoutMs <= 0 {
		return fmt.Errorf("config: request_timeout_ms must be positive, got %d", c.RequestTimeoutMs)
	}
	if c.IdentityDBPath == "" {
		return fmt.Errorf("config: identity_db_path must not be empty")
	}
	if c.MaxRouteBytes <= 0 {
		return fmt.Errorf("config: max_route_bytes must be positive, got %d", c.MaxRouteBytes)
	}
	if c.MaxRouteRows <= 0 {
		return fmt.Errorf("config: max_route_rows must be positive, got %d", c.MaxRouteRows)
	}

	// Soft sanity check: a packet of packet_size fixes must plausibly fit
	// the aggregator's default 1 MiB body limit. This is an approximation
	// (see assumedMaxJSONBytesPerFix's doc comment); it exists only to
	// catch grossly misconfigured packet sizes early, not to guarantee
	// exact byte accounting.
	if int64(c.PacketSize)*assumedMaxJSONBytesPerFix > assumedAggregatorMaxBodyBytes {
		return fmt.Errorf("config: packet_size %d is likely to produce packets exceeding the aggregator's default max_body_bytes (%d); reduce packet_size", c.PacketSize, assumedAggregatorMaxBodyBytes)
	}

	return nil
}

// ValidateAggregatorURL validates that raw is an absolute http/https URL
// with no credentials, query, or fragment, and a root path only, returning
// the URL with /api/v1/telemetry appended (SIM-01).
func ValidateAggregatorURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("URL must be absolute (include a host)")
	}
	if u.User != nil {
		return "", fmt.Errorf("URL must not contain credentials")
	}
	if u.RawQuery != "" {
		return "", fmt.Errorf("URL must not contain a query string")
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("URL must not contain a fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("URL must have root path only (no sub-path), got %q", u.Path)
	}
	base := u.Scheme + "://" + u.Host
	return base + "/api/v1/telemetry", nil
}
