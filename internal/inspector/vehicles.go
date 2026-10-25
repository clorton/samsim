package inspector

import (
	"context"

	"gpsdemo/internal/inspectclient"
)

// VehicleListEntry is one row of the vehicles report.
type VehicleListEntry struct {
	VehicleID         string  `json:"vehicle_id"`
	LatestTimestampMs *int64  `json:"latest_timestamp_ms"`
	ActiveTripID      *string `json:"active_trip_id"`
	Revision          int64   `json:"revision"`
}

// VehiclesReport is the "vehicles" command's data payload.
type VehiclesReport struct {
	Items []VehicleListEntry `json:"items"`
}

// VehiclesParams holds the "vehicles" command's options.
type VehiclesParams struct {
	ActiveOnly bool
}

// RunVehicles implements `gpsinspect vehicles` (INS-02): best-effort,
// cursor-paginated, deduplicated by vehicle_id, with no invented global
// revision (the global vehicle list spans multiple independent reads, so it
// is not a single coherent snapshot).
func RunVehicles(ctx context.Context, a *App, p VehiclesParams) (*VehiclesReport, []string, error) {
	seen := map[string]bool{}
	var items []VehicleListEntry
	cursor := ""
	collector := inspectclient.NewPageCollector(100000)
	warnings := []string{"the global vehicle list is best-effort across pages, not a single transactional snapshot"}

	for {
		env, err := a.Client.ListVehicles(ctx, inspectclient.ListVehiclesParams{
			ActiveOnly: p.ActiveOnly,
			Limit:      a.Config.PageSize,
			Cursor:     cursor,
		})
		if err != nil {
			return nil, nil, err
		}
		for _, v := range env.Items {
			if seen[v.VehicleID] {
				continue
			}
			seen[v.VehicleID] = true
			entry := VehicleListEntry{VehicleID: v.VehicleID, ActiveTripID: v.ActiveTripID, Revision: v.Revision}
			items = append(items, entry)
		}
		if err := collector.CheckCount(len(items)); err != nil {
			return nil, nil, err
		}
		if env.NextCursor == nil {
			break
		}
		if err := collector.CheckCursor(*env.NextCursor); err != nil {
			return nil, nil, err
		}
		cursor = *env.NextCursor
	}

	// Fetch latest_timestamp_ms for display by looking up each vehicle's
	// detail. This is deliberately a best-effort enrichment, not part of the
	// coherent-read protocol (the spec's vehicles command has no pinned
	// revision); a vehicle that changes between the list and detail fetch is
	// not an error here, since the whole list is already documented as
	// best-effort.
	for i := range items {
		v, err := a.Client.GetVehicle(ctx, items[i].VehicleID, nil)
		if err != nil {
			if inspectclient.IsCode(err, "NOT_FOUND") {
				continue // vehicle removed between list and detail fetch
			}
			return nil, nil, err
		}
		if v.LatestFix != nil {
			ts := v.LatestFix.TimestampMs
			items[i].LatestTimestampMs = &ts
		}
		items[i].Revision = v.Revision
	}

	return &VehiclesReport{Items: items}, warnings, nil
}
