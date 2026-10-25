package inspector

import (
	"context"

	"gpsdemo/internal/inspectclient"
)

// TripsReport is the "trips" command's data payload.
type TripsReport struct {
	VehicleID  string                      `json:"vehicle_id"`
	DatabaseID string                      `json:"database_id"`
	Revision   int64                       `json:"revision"`
	Items      []inspectclient.TripSummary `json:"items"`
}

// TripsParams holds the "trips" command's options.
type TripsParams struct {
	IncludeMerged bool
	Status        string
}

// RunTrips implements `gpsinspect trips <vehicle-id>` (INS-02): collects
// the vehicle's current trip history (optionally including MERGED audit
// predecessors and filtered by status) at one coherent revision, per
// section 4's protocol.
func RunTrips(ctx context.Context, a *App, vehicleID string, p TripsParams) (*TripsReport, error) {
	items, d, r, err := inspectclient.RunCoherent(ctx, a.Client, vehicleID, a.Config.RevisionAttempts,
		func(ctx context.Context, d string, r int64) ([]inspectclient.TripSummary, error) {
			return collectAllTrips(ctx, a, vehicleID, p, r)
		})
	if err != nil {
		return nil, err
	}
	return &TripsReport{VehicleID: vehicleID, DatabaseID: d, Revision: r, Items: items}, nil
}

// collectAllTrips pages through the vehicle's trip list pinned at revision
// r, validating every page's database_id/revision and detecting cursor
// cycles, per section 4.
func collectAllTrips(ctx context.Context, a *App, vehicleID string, p TripsParams, r int64) ([]inspectclient.TripSummary, error) {
	var items []inspectclient.TripSummary
	cursor := ""
	collector := inspectclient.NewPageCollector(100000)
	for {
		env, err := a.Client.ListTrips(ctx, vehicleID, inspectclient.ListTripsParams{
			IncludeMerged: p.IncludeMerged,
			Status:        p.Status,
			Limit:         a.Config.PageSize,
			Cursor:        cursor,
			AtRevision:    &r,
		})
		if err != nil {
			return nil, err
		}
		if env.Revision != r {
			return nil, &inspectclient.Error{Kind: inspectclient.KindProtocol, Message: "trips page revision does not match pinned revision"}
		}
		items = append(items, env.Items...)
		if err := collector.CheckCount(len(items)); err != nil {
			return nil, err
		}
		if env.NextCursor == nil {
			break
		}
		if err := collector.CheckCursor(*env.NextCursor); err != nil {
			return nil, err
		}
		cursor = *env.NextCursor
	}
	return items, nil
}
