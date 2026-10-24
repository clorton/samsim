package aggregator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gpsdemo/internal/apierr"
	"gpsdemo/internal/events"
	"gpsdemo/internal/storage"
	"gpsdemo/internal/trips"
)

// CloseResult is the response body for a trip closure request (AG-06).
type CloseResult struct {
	TripID             string           `json:"trip_id"`
	Status             string           `json:"status"`
	CloseReason        string           `json:"close_reason"`
	Revision           int64            `json:"revision"`
	ForcedClose        bool             `json:"forced_close"`
	Completeness       *CompletenessDTO `json:"completeness,omitempty"`
	SupersededByTripID *string          `json:"superseded_by_trip_id,omitempty"`
}

// CompletenessDTO mirrors AG-06's completeness assessment response shape.
type CompletenessDTO struct {
	TripID                string               `json:"trip_id"`
	Revision              int64                `json:"revision"`
	ExpectedFixIntervalMs int64                `json:"expected_fix_interval_ms"`
	MissingFixCount       int64                `json:"missing_fix_count"`
	MissingIntervals      []MissingIntervalDTO `json:"missing_intervals"`
	Complete              bool                 `json:"complete"`
	BoundaryCompleteness  string               `json:"boundary_completeness"`
	Assessment            string               `json:"assessment"`
}

// MissingIntervalDTO mirrors one reported internal gap.
type MissingIntervalDTO struct {
	StartMs      int64 `json:"start_ms"`
	EndMs        int64 `json:"end_ms"`
	MissingCount int64 `json:"missing_count"`
}

func toCompletenessDTO(tripID string, revision int64, expectedIntervalMs int64, c trips.Completeness) *CompletenessDTO {
	dto := &CompletenessDTO{
		TripID:                tripID,
		Revision:              revision,
		ExpectedFixIntervalMs: expectedIntervalMs,
		MissingFixCount:       c.MissingFixCount,
		Complete:              c.Complete,
		BoundaryCompleteness:  "UNKNOWN",
		Assessment:            "NO_INTERNAL_GAPS",
	}
	if !c.Complete {
		dto.Assessment = "INTERNAL_GAPS"
	}
	for _, mi := range c.MissingIntervals {
		dto.MissingIntervals = append(dto.MissingIntervals, MissingIntervalDTO{
			StartMs: mi.StartMs, EndMs: mi.EndMs, MissingCount: mi.MissingCount,
		})
	}
	return dto
}

// GetCompleteness computes AG-06 completeness for a trip from a coherent
// read transaction.
func (s *Service) GetCompleteness(ctx context.Context, tripID string) (*CompletenessDTO, error) {
	return storage.ReadTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*CompletenessDTO, error) {
		trip, err := storage.GetTrip(ctx, tx, tripID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, apierr.Newf(apierr.CodeNotFound, "trip %q not found", tripID)
			}
			return nil, err
		}
		vehicle, err := storage.GetVehicle(ctx, tx, trip.VehicleID)
		if err != nil {
			return nil, err
		}
		timestamps, err := storage.ListTripFixTimestamps(ctx, tx, tripID)
		if err != nil {
			return nil, err
		}
		c := trips.AnalyzeCompleteness(timestamps, s.Store.ExpectedFixIntervalMs)
		return toCompletenessDTO(tripID, vehicle.Revision, s.Store.ExpectedFixIntervalMs, c), nil
	})
}

// closeTxResult threads the close transaction's outcome to the post-commit
// publish step.
type closeTxResult struct {
	response *CloseResult
	event    *events.Envelope
}

// Close implements AG-06's POST /api/v1/trips/{id}/close: validates
// identity/state, enforces the optimistic revision precondition, recomputes
// completeness, and (absent internal gaps or with force=true) performs the
// atomic closure: freeze membership, insert the manual barrier, clear the
// active pointer if necessary, bump revision once, and emit closure events.
func (s *Service) Close(ctx context.Context, tripID string, expectedRevision int64, force bool) (*CloseResult, error) {
	// We don't yet know the vehicle for this trip; look it up outside the
	// lock first (trips are immutable once created as to which vehicle they
	// belong), then acquire the per-vehicle lock before the writer
	// coordinator, per AG-01.
	vehicleID, err := storage.ReadTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (string, error) {
		trip, err := storage.GetTrip(ctx, tx, tripID)
		if err != nil {
			return "", err
		}
		return trip.VehicleID, nil
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, apierr.Newf(apierr.CodeNotFound, "trip %q not found", tripID)
		}
		return nil, err
	}

	unlock := s.lockVehicle(vehicleID)
	defer unlock()

	result, err := storage.WithWriteTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*closeTxResult, error) {
		return s.closeTx(ctx, tx, tripID, expectedRevision, force)
	}, func(r *closeTxResult) {
		if r.event != nil {
			s.Hub.Publish(*r.event)
		}
	})
	if err != nil {
		return nil, err
	}
	return result.response, nil
}

func (s *Service) closeTx(ctx context.Context, tx *sql.Tx, tripID string, expectedRevision int64, force bool) (*closeTxResult, error) {
	trip, err := storage.GetTrip(ctx, tx, tripID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, apierr.Newf(apierr.CodeNotFound, "trip %q not found", tripID)
		}
		return nil, err
	}

	vehicle, err := storage.GetVehicle(ctx, tx, trip.VehicleID)
	if err != nil {
		return nil, err
	}

	// CLOSED/MERGED: do not silently close the successor.
	if trip.Status == storage.TripStatusClosed && trip.CloseReason == storage.CloseReasonMerged {
		successor, rerr := storage.ResolveTerminalSuccessor(ctx, tx, tripID)
		if rerr != nil {
			return nil, rerr
		}
		return nil, apierr.Newf(apierr.CodeTripSuperseded, "trip %q has been merged", tripID).
			WithDetails(map[string]any{"successor_trip_id": successor})
	}

	// CLOSED/MANUAL: idempotent no-op, regardless of supplied expected
	// revision (supports retry after a lost close response).
	if trip.Status == storage.TripStatusClosed && trip.CloseReason == storage.CloseReasonManual {
		resp := &CloseResult{
			TripID: tripID, Status: trip.Status, CloseReason: trip.CloseReason,
			Revision: vehicle.Revision, ForcedClose: trip.ForcedClose,
		}
		return &closeTxResult{response: resp, event: nil}, nil
	}

	// Stale revision -> 409, even for force=true.
	if expectedRevision != vehicle.Revision {
		return nil, apierr.Newf(apierr.CodeRevisionConflict,
			"expected_revision %d does not match current revision %d", expectedRevision, vehicle.Revision).
			WithDetails(map[string]any{"revision": vehicle.Revision})
	}

	timestamps, err := storage.ListTripFixTimestamps(ctx, tx, tripID)
	if err != nil {
		return nil, err
	}
	completeness := trips.AnalyzeCompleteness(timestamps, s.Store.ExpectedFixIntervalMs)

	if !completeness.Complete && !force {
		dto := toCompletenessDTO(tripID, vehicle.Revision, s.Store.ExpectedFixIntervalMs, completeness)
		return nil, (&apierr.Error{
			Code:    apierr.CodeTripIncomplete,
			Message: "trip has missing internal fixes",
		}).WithDetails(map[string]any{
			"requires_confirmation": true,
			"revision":              vehicle.Revision,
			"completeness":          dto,
		})
	}

	nowMs := s.Clock.NowMs()
	newRevision := vehicle.Revision + 1

	missingCount := completeness.MissingFixCount
	updated := *trip
	updated.Status = storage.TripStatusClosed
	updated.CloseReason = storage.CloseReasonManual
	updated.UpdatedRevision = newRevision
	updated.ClosedAtMs = &nowMs
	updated.ForcedClose = force
	updated.MissingFixCountAtClose = &missingCount
	if err := storage.UpdateTrip(ctx, tx, updated); err != nil {
		return nil, err
	}

	if err := storage.InsertManualBarrier(ctx, tx, storage.ManualBarrierRow{
		TripID: tripID, VehicleID: trip.VehicleID, StartMs: trip.StartMs, EndMs: trip.EndMs,
		ClosedRevision: newRevision,
	}); err != nil {
		return nil, err
	}

	newVehicle := *vehicle
	newVehicle.Revision = newRevision
	newVehicle.UpdatedAtMs = nowMs
	if vehicle.ActiveTripID != nil && *vehicle.ActiveTripID == tripID {
		newVehicle.ActiveTripID = nil
	}
	if err := storage.UpdateVehicle(ctx, tx, newVehicle); err != nil {
		return nil, err
	}

	env := &events.Envelope{
		EventID:     fmt.Sprintf("%s:%s:%d", s.Store.DatabaseID, trip.VehicleID, newRevision),
		DatabaseID:  s.Store.DatabaseID,
		VehicleID:   trip.VehicleID,
		Revision:    newRevision,
		Type:        "vehicle.changed",
		TimestampMs: nowMs,
		Changes: []events.Change{
			{Type: events.ChangeTripClosed, TripID: tripID, CloseReason: storage.CloseReasonManual},
			{Type: events.ChangeVehicleUpdated, ActiveTripID: newVehicle.ActiveTripID, LatestTimestampMs: vehicle.LatestFixTimestampMs},
		},
	}

	resp := &CloseResult{
		TripID: tripID, Status: updated.Status, CloseReason: updated.CloseReason,
		Revision: newRevision, ForcedClose: force,
		Completeness: toCompletenessDTO(tripID, newRevision, s.Store.ExpectedFixIntervalMs, completeness),
	}

	return &closeTxResult{response: resp, event: env}, nil
}
