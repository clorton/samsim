package inspector

import (
	"context"

	"gpsdemo/internal/inspectclient"
)

// GapsReport is the "gaps" command's data payload.
type GapsReport struct {
	DatabaseID            string                             `json:"database_id"`
	Revision              int64                              `json:"revision"`
	TripID                string                             `json:"trip_id"`
	ExpectedFixIntervalMs int64                              `json:"expected_fix_interval_ms"`
	MissingFixCount       int64                              `json:"missing_fix_count"`
	Complete              bool                               `json:"complete"`
	BoundaryCompleteness  string                             `json:"boundary_completeness"`
	Assessment            string                             `json:"assessment"`
	TotalIntervals        int                                `json:"total_intervals"`
	DisplayedIntervals    []inspectclient.MissingIntervalDTO `json:"displayed_intervals"`
	Truncated             bool                               `json:"truncated"`
}

// GapsParams holds the "gaps" command's options.
type GapsParams struct {
	IntervalLimit int // 0 means use the default (20)
}

const (
	defaultGapsIntervalLimit = 20
	maxGapsIntervalLimit     = 1000
)

// RunGaps implements `gpsinspect gaps <trip-id>` (INS-02). The published
// completeness API returns the full missing_intervals list in one response
// (no pagination exists for this resource today), so "collect pinned pages
// before applying display limit" degenerates to: fetch once at the pinned
// revision, then truncate for display only; counts/totals always reflect
// the full (untruncated) set.
func RunGaps(ctx context.Context, a *App, tripID string, p GapsParams) (*GapsReport, error) {
	limit := p.IntervalLimit
	if limit <= 0 {
		limit = defaultGapsIntervalLimit
	}
	if limit > maxGapsIntervalLimit {
		limit = maxGapsIntervalLimit
	}

	report, d, r, err := inspectclient.RunCoherentForTrip(ctx, a.Client, tripID, a.Config.RevisionAttempts,
		func(ctx context.Context, d string, r int64, trip *inspectclient.TripDetail) (*GapsReport, error) {
			if trip.Completeness == nil {
				return nil, &inspectclient.Error{Kind: inspectclient.KindProtocol, Message: "server omitted required completeness detail for trip " + tripID}
			}
			c := trip.Completeness
			total := len(c.MissingIntervals)
			displayed := c.MissingIntervals
			truncated := false
			if total > limit {
				displayed = c.MissingIntervals[:limit]
				truncated = true
			}
			return &GapsReport{
				TripID:                tripID,
				ExpectedFixIntervalMs: c.ExpectedFixIntervalMs,
				MissingFixCount:       c.MissingFixCount,
				Complete:              c.Complete,
				BoundaryCompleteness:  c.BoundaryCompleteness,
				Assessment:            c.Assessment,
				TotalIntervals:        total,
				DisplayedIntervals:    displayed,
				Truncated:             truncated,
			}, nil
		})
	if err != nil {
		return nil, err
	}
	report.DatabaseID = d
	report.Revision = r
	return report, nil
}
