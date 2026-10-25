// Package inspectvalidate implements gpsinspect's `validate` command
// (INS-05): an independent reference model and check suite (V01..V10) built
// entirely from the aggregator's public read APIs, and a baseline
// file format for cross-run immutability/monotonicity comparisons (V09,
// V10). This package deliberately does not import internal/trips or any
// other aggregator-internal reconstruction code: "Do not copy the
// aggregator's implementation into this package."
package inspectvalidate

import (
	"context"
	"strconv"

	"gpsdemo/internal/inspectclient"
)

// Observation is one coherent, fully-collected snapshot of a vehicle's
// state at database D, revision R, gathered via section 4's coherent-read
// protocol: vehicle/config, every current and MERGED-audit trip's detail,
// every trip's ordered geometry, and every raw fix (assigned and
// quarantined).
type Observation struct {
	DatabaseID string
	VehicleID  string
	Revision   int64

	TripGapSeconds        int64
	ExpectedFixIntervalMs int64

	LatestFix *inspectclient.FixDTO

	ManualBarriers []inspectclient.ManualBarrierDTO

	// Trips includes every trip returned by include_merged=true: current
	// (ACTIVE/INACTIVE/CLOSED-MANUAL) and CLOSED/MERGED audit predecessors.
	Trips []inspectclient.TripDetail

	// Geometry maps trip_id to that trip's ordered fix snapshot (its own
	// frozen geometry, including MERGED predecessors' original geometry).
	Geometry map[string][]inspectclient.FixDTO

	// RawFixes is every raw fix for the vehicle (assignment=all), ordered by
	// timestamp ascending, including quarantined records.
	RawFixes []inspectclient.FixDTO
}

// CollectOptions bounds a Collect call per section 4/section 8's resource
// limits.
type CollectOptions struct {
	PageSize         int
	RevisionAttempts int
	MaxFixes         int // across all geometry+raw pages combined, per spec
}

// ErrResourceLimitExceeded is returned when collection would exceed the
// configured max-fixes (or 100000-item nonfix) bound; this is an
// incomplete-collection error, not a partial pass.
type ErrResourceLimitExceeded struct {
	Resource string
	Limit    int
}

func (e *ErrResourceLimitExceeded) Error() string {
	return "validate: " + e.Resource + " exceeded the configured limit of " + strconv.Itoa(e.Limit)
}

// Collect gathers a full coherent Observation for vehicleID using the
// shared coherent-read protocol (section 4): one snapshot determines D/R,
// then every further read is pinned at R, then a final reread confirms no
// commit landed during collection.
func Collect(ctx context.Context, client *inspectclient.Client, vehicleID string, opts CollectOptions) (*Observation, error) {
	obs, _, _, err := inspectclient.RunCoherent(ctx, client, vehicleID, opts.RevisionAttempts,
		func(ctx context.Context, d string, r int64) (*Observation, error) {
			return collectAt(ctx, client, vehicleID, d, r, opts)
		})
	if err != nil {
		return nil, err
	}
	return obs, nil
}

func collectAt(ctx context.Context, client *inspectclient.Client, vehicleID, databaseID string, revision int64, opts CollectOptions) (*Observation, error) {
	snap, err := client.GetSnapshot(ctx, vehicleID)
	if err != nil {
		return nil, err
	}
	if snap.DatabaseID != databaseID || snap.Revision != revision {
		return nil, &inspectclient.Error{Kind: inspectclient.KindAPI, Code: "REVISION_CONFLICT", Message: "snapshot changed during validate collection"}
	}

	obs := &Observation{
		DatabaseID:            databaseID,
		VehicleID:             vehicleID,
		Revision:              revision,
		TripGapSeconds:        snap.Vehicle.TripGapSeconds,
		ExpectedFixIntervalMs: snap.Vehicle.ExpectedFixIntervalMs,
		LatestFix:             snap.Vehicle.LatestFix,
		ManualBarriers:        snap.ManualBarriers,
		Geometry:              map[string][]inspectclient.FixDTO{},
	}

	tripSummaries, err := collectAllTripsAt(ctx, client, vehicleID, revision, opts.PageSize)
	if err != nil {
		return nil, err
	}

	totalFixes := 0
	for _, ts := range tripSummaries {
		detail, err := client.GetTripDetail(ctx, ts.TripID, &revision)
		if err != nil {
			return nil, err
		}
		obs.Trips = append(obs.Trips, *detail)

		geom, err := client.GetTripFixes(ctx, ts.TripID, &revision)
		if err != nil {
			return nil, err
		}
		totalFixes += len(geom.Items)
		if totalFixes > opts.MaxFixes {
			return nil, &ErrResourceLimitExceeded{Resource: "trip geometry fixes", Limit: opts.MaxFixes}
		}
		obs.Geometry[ts.TripID] = geom.Items
	}

	rawFixes, err := collectAllFixesAt(ctx, client, vehicleID, revision, opts.PageSize, opts.MaxFixes, totalFixes)
	if err != nil {
		return nil, err
	}
	obs.RawFixes = rawFixes

	return obs, nil
}

func collectAllTripsAt(ctx context.Context, client *inspectclient.Client, vehicleID string, revision int64, pageSize int) ([]inspectclient.TripSummary, error) {
	var items []inspectclient.TripSummary
	cursor := ""
	collector := inspectclient.NewPageCollector(100000)
	for {
		env, err := client.ListTrips(ctx, vehicleID, inspectclient.ListTripsParams{
			IncludeMerged: true, Limit: pageSize, Cursor: cursor, AtRevision: &revision,
		})
		if err != nil {
			return nil, err
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

func collectAllFixesAt(ctx context.Context, client *inspectclient.Client, vehicleID string, revision int64, pageSize, maxFixes, alreadyCounted int) ([]inspectclient.FixDTO, error) {
	var items []inspectclient.FixDTO
	cursor := ""
	collector := inspectclient.NewPageCollector(1 << 30) // raw fixes are bounded by maxFixes, not the 100000 nonfix cap
	total := alreadyCounted
	for {
		env, err := client.ListVehicleFixes(ctx, vehicleID, inspectclient.ListFixesParams{
			Assignment: "all", Limit: pageSize, Cursor: cursor, AtRevision: &revision,
		})
		if err != nil {
			return nil, err
		}
		items = append(items, env.Items...)
		total += len(env.Items)
		if total > maxFixes {
			return nil, &ErrResourceLimitExceeded{Resource: "raw fixes", Limit: maxFixes}
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
