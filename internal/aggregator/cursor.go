package aggregator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// cursorPayload is the opaque pagination cursor contents (AG-07): it binds
// the query/filter/order and, for per-vehicle data, the revision it was
// issued against, so that the aggregator can detect when ingestion
// invalidates an in-progress paginated read.
type cursorPayload struct {
	Kind       string `json:"kind"` // "fixes" | "trips" | "vehicles"
	VehicleID  string `json:"vehicle_id,omitempty"`
	Revision   int64  `json:"revision,omitempty"`
	FilterHash string `json:"filter_hash"`

	// Sort-key continuation, interpreted per Kind.
	LastTimestampMs int64  `json:"last_timestamp_ms,omitempty"`
	LastStartMs     int64  `json:"last_start_ms,omitempty"`
	LastTripID      string `json:"last_trip_id,omitempty"`
	LastVehicleID   string `json:"last_vehicle_id,omitempty"`
}

func encodeCursor(p cursorPayload) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("aggregator: encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeCursor(s string) (cursorPayload, error) {
	var p cursorPayload
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return p, fmt.Errorf("aggregator: malformed cursor: %w", err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("aggregator: malformed cursor: %w", err)
	}
	return p, nil
}

// clampLimit applies AG-07's default/maximum pagination limits.
func clampLimit(requested int) int {
	const defaultLimit = 100
	const maxLimit = 1000
	if requested <= 0 {
		return defaultLimit
	}
	if requested > maxLimit {
		return maxLimit
	}
	return requested
}
