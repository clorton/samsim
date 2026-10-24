// Package config loads and validates the aggregator's static configuration.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// SimulatorEndpoint is a statically trusted control-plane target for AG-09.
type SimulatorEndpoint struct {
	SimulatorID string `yaml:"simulator_id"`
	ControlURL  string `yaml:"control_url"`
	VehicleID   string `yaml:"vehicle_id"`
}

// Config is the aggregator's complete startup configuration (AG-01).
type Config struct {
	Listen                 string              `yaml:"listen"`
	DatabasePath           string              `yaml:"database_path"`
	TripGapSeconds         int64               `yaml:"trip_gap_seconds"`
	ExpectedFixIntervalMs  int64               `yaml:"expected_fix_interval_ms"`
	MaxBodyBytes           int64               `yaml:"max_body_bytes"`
	MaxFixesPerPacket      int64               `yaml:"max_fixes_per_packet"`
	SQLiteBusyTimeoutMs    int64               `yaml:"sqlite_busy_timeout_ms"`
	WebSocketQueueCapacity int                 `yaml:"websocket_queue_capacity"`
	Simulators             []SimulatorEndpoint `yaml:"simulators"`
}

// Default returns the spec's default configuration (AG-01).
func Default() Config {
	return Config{
		Listen:                 "127.0.0.1:8080",
		DatabasePath:           "./data/gps.sqlite",
		TripGapSeconds:         600,
		ExpectedFixIntervalMs:  1000,
		MaxBodyBytes:           1048576,
		MaxFixesPerPacket:      10000,
		SQLiteBusyTimeoutMs:    5000,
		WebSocketQueueCapacity: 256,
		Simulators:             nil,
	}
}

// Load reads a YAML config file, applying spec defaults for any field the
// file does not set, then validates the result.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: read %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects nonpositive/overflowing durations and limits (AG-01).
func (c Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("config: listen must not be empty")
	}
	if c.DatabasePath == "" {
		return fmt.Errorf("config: database_path must not be empty")
	}
	if c.TripGapSeconds <= 0 {
		return fmt.Errorf("config: trip_gap_seconds must be positive, got %d", c.TripGapSeconds)
	}
	// trip_gap_seconds * 1000 must not overflow int64.
	const maxSeconds = (1 << 62) / 1000
	if c.TripGapSeconds > maxSeconds {
		return fmt.Errorf("config: trip_gap_seconds overflows when converted to milliseconds: %d", c.TripGapSeconds)
	}
	if c.ExpectedFixIntervalMs <= 0 {
		return fmt.Errorf("config: expected_fix_interval_ms must be positive, got %d", c.ExpectedFixIntervalMs)
	}
	if c.MaxBodyBytes <= 0 {
		return fmt.Errorf("config: max_body_bytes must be positive, got %d", c.MaxBodyBytes)
	}
	if c.MaxFixesPerPacket <= 0 {
		return fmt.Errorf("config: max_fixes_per_packet must be positive, got %d", c.MaxFixesPerPacket)
	}
	if c.SQLiteBusyTimeoutMs <= 0 {
		return fmt.Errorf("config: sqlite_busy_timeout_ms must be positive, got %d", c.SQLiteBusyTimeoutMs)
	}
	if c.WebSocketQueueCapacity <= 0 {
		return fmt.Errorf("config: websocket_queue_capacity must be positive, got %d", c.WebSocketQueueCapacity)
	}
	seen := make(map[string]bool, len(c.Simulators))
	for _, s := range c.Simulators {
		if s.SimulatorID == "" {
			return fmt.Errorf("config: simulators entry missing simulator_id")
		}
		if seen[s.SimulatorID] {
			return fmt.Errorf("config: duplicate simulator_id %q", s.SimulatorID)
		}
		seen[s.SimulatorID] = true
		if s.ControlURL == "" {
			return fmt.Errorf("config: simulator %q missing control_url", s.SimulatorID)
		}
		if s.VehicleID == "" {
			return fmt.Errorf("config: simulator %q missing vehicle_id", s.SimulatorID)
		}
	}
	return nil
}

// TripGapMs returns the configured trip gap in milliseconds.
func (c Config) TripGapMs() int64 {
	return c.TripGapSeconds * 1000
}
