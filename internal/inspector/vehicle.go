package inspector

import (
	"context"

	"gpsdemo/internal/inspectclient"
)

// VehicleReport is the "vehicle" command's data payload.
type VehicleReport struct {
	VehicleID             string                `json:"vehicle_id"`
	DatabaseID            string                `json:"database_id"`
	Revision              int64                 `json:"revision"`
	LatestFix             *inspectclient.FixDTO `json:"latest_fix"`
	ActiveTripID          *string               `json:"active_trip_id"`
	TripGapSeconds        int64                 `json:"trip_gap_seconds"`
	ExpectedFixIntervalMs int64                 `json:"expected_fix_interval_ms"`
}

// RunVehicle implements `gpsinspect vehicle <id>` (INS-02). This is a
// single-read command: the vehicle detail response is already internally
// coherent (one read-transaction), so the shared multi-request coherent-read
// protocol does not apply, per spec: "Single-read vehicle command may
// directly use its coherent state response."
func RunVehicle(ctx context.Context, a *App, vehicleID string) (*VehicleReport, error) {
	v, err := a.Client.GetVehicle(ctx, vehicleID, nil)
	if err != nil {
		return nil, err
	}
	return &VehicleReport{
		VehicleID:             v.VehicleID,
		DatabaseID:            v.DatabaseID,
		Revision:              v.Revision,
		LatestFix:             v.LatestFix,
		ActiveTripID:          v.ActiveTripID,
		TripGapSeconds:        v.TripGapSeconds,
		ExpectedFixIntervalMs: v.ExpectedFixIntervalMs,
	}, nil
}
