package aggregator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gpsdemo/internal/apierr"
	"gpsdemo/internal/storage"
	"gpsdemo/internal/trips"
)

// ListVehiclesParams holds GET /api/v1/vehicles query parameters.
type ListVehiclesParams struct {
	ActiveOnly bool
	Limit      int
	Cursor     string
}

// ListVehicles implements GET /api/v1/vehicles (AG-07): all known vehicles,
// optionally filtered to those with a nonnull active_trip_id, with
// best-effort cursor pagination (no invented global revision).
func (s *Service) ListVehicles(ctx context.Context, p ListVehiclesParams) (*ListEnvelope[VehicleListItem], error) {
	limit := clampLimit(p.Limit)

	afterID := ""
	if p.Cursor != "" {
		cp, err := decodeCursor(p.Cursor)
		if err != nil {
			return nil, apierr.Newf(apierr.CodeInvalidRequest, "%v", err)
		}
		if cp.Kind != "vehicles" {
			return nil, apierr.New(apierr.CodeInvalidRequest, "cursor is not a vehicles cursor")
		}
		afterID = cp.LastVehicleID
	}

	return storage.ReadTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*ListEnvelope[VehicleListItem], error) {
		// Fetch one extra row to determine whether a next page exists.
		rows, err := storage.ListVehiclesAfter(ctx, tx, afterID, limit+1)
		if err != nil {
			return nil, err
		}

		var items []VehicleListItem
		for _, v := range rows {
			if p.ActiveOnly && v.ActiveTripID == nil {
				continue
			}
			items = append(items, VehicleListItem{VehicleID: v.VehicleID, ActiveTripID: v.ActiveTripID, Revision: v.Revision})
			if len(items) == limit {
				break
			}
		}

		var nextCursor *string
		if len(rows) > limit {
			lastIncluded := rows[limit-1]
			c, err := encodeCursor(cursorPayload{Kind: "vehicles", LastVehicleID: lastIncluded.VehicleID})
			if err != nil {
				return nil, err
			}
			nextCursor = &c
		}

		return &ListEnvelope[VehicleListItem]{Items: items, NextCursor: nextCursor, DatabaseID: s.Store.DatabaseID}, nil
	})
}

// GetVehicle implements GET /api/v1/vehicles/{id} (AG-07).
func (s *Service) GetVehicle(ctx context.Context, vehicleID string, atRevision *int64) (*VehicleDTO, error) {
	return storage.ReadTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*VehicleDTO, error) {
		v, err := storage.GetVehicle(ctx, tx, vehicleID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, apierr.Newf(apierr.CodeNotFound, "vehicle %q not found", vehicleID)
			}
			return nil, err
		}
		if err := checkAtRevision(atRevision, v.Revision); err != nil {
			return nil, err
		}
		return s.vehicleDTOFromRow(ctx, tx, v)
	})
}

func (s *Service) vehicleDTOFromRow(ctx context.Context, tx *sql.Tx, v *storage.VehicleRow) (*VehicleDTO, error) {
	dto := &VehicleDTO{
		VehicleID:             v.VehicleID,
		ActiveTripID:          v.ActiveTripID,
		Revision:              v.Revision,
		DatabaseID:            s.Store.DatabaseID,
		TripGapSeconds:        s.Store.TripGapSeconds,
		ExpectedFixIntervalMs: s.Store.ExpectedFixIntervalMs,
	}
	if v.LatestFixTimestampMs != nil {
		fix, err := storage.GetFix(ctx, tx, v.VehicleID, *v.LatestFixTimestampMs)
		if err != nil {
			return nil, err
		}
		var currentTripID *string
		if id, err := storage.GetCurrentTripIDForFix(ctx, tx, v.VehicleID, *v.LatestFixTimestampMs); err == nil {
			currentTripID = &id
		} else if !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		}
		f := fixDTOFromRow(*fix, currentTripID)
		dto.LatestFix = &f
	}
	return dto, nil
}

// checkAtRevision implements AG-07's at_revision precondition: if atRevision
// is non-nil and differs from current, return 409 REVISION_CONFLICT.
func checkAtRevision(atRevision *int64, current int64) error {
	if atRevision != nil && *atRevision != current {
		return apierr.Newf(apierr.CodeRevisionConflict,
			"at_revision %d does not match current revision %d", *atRevision, current).
			WithDetails(map[string]any{"revision": current})
	}
	return nil
}

// GetSnapshot implements GET /api/v1/vehicles/{id}/snapshot (AG-07): one
// coherent read-transaction snapshot of the vehicle, all current trip
// summaries, and immutable manual barriers.
func (s *Service) GetSnapshot(ctx context.Context, vehicleID string) (*SnapshotDTO, error) {
	return storage.ReadTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*SnapshotDTO, error) {
		v, err := storage.GetVehicle(ctx, tx, vehicleID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, apierr.Newf(apierr.CodeNotFound, "vehicle %q not found", vehicleID)
			}
			return nil, err
		}
		vehicleDTO, err := s.vehicleDTOFromRow(ctx, tx, v)
		if err != nil {
			return nil, err
		}

		tripRows, err := storage.ListCurrentTripsByVehicle(ctx, tx, vehicleID, false, "")
		if err != nil {
			return nil, err
		}
		var trips []TripSummary
		for _, t := range tripRows {
			trips = append(trips, tripSummaryFromRow(t))
		}

		barrierRows, err := storage.ListManualBarriersByVehicle(ctx, tx, vehicleID)
		if err != nil {
			return nil, err
		}
		var barriers []ManualBarrierDTO
		for _, b := range barrierRows {
			barriers = append(barriers, ManualBarrierDTO{
				TripID: b.TripID, StartMs: b.StartMs, EndMs: b.EndMs, ClosedRevision: b.ClosedRevision,
			})
		}

		return &SnapshotDTO{
			Vehicle:        *vehicleDTO,
			Trips:          trips,
			ManualBarriers: barriers,
			Revision:       v.Revision,
			DatabaseID:     s.Store.DatabaseID,
		}, nil
	})
}

// ListTripsParams holds GET /api/v1/vehicles/{id}/trips query parameters.
type ListTripsParams struct {
	IncludeMerged bool
	Status        string
	Limit         int
	Cursor        string
	AtRevision    *int64
}

// ListTrips implements GET /api/v1/vehicles/{id}/trips (AG-07).
func (s *Service) ListTrips(ctx context.Context, vehicleID string, p ListTripsParams) (*ListEnvelope[TripSummary], error) {
	limit := clampLimit(p.Limit)

	var afterStartMs *int64
	afterTripID := ""
	if p.Cursor != "" {
		cp, err := decodeCursor(p.Cursor)
		if err != nil {
			return nil, apierr.Newf(apierr.CodeInvalidRequest, "%v", err)
		}
		if cp.Kind != "trips" || cp.VehicleID != vehicleID {
			return nil, apierr.New(apierr.CodeInvalidRequest, "cursor does not match this vehicle/trips query")
		}
		afterStartMs = &cp.LastStartMs
		afterTripID = cp.LastTripID
	}

	return storage.ReadTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*ListEnvelope[TripSummary], error) {
		v, err := storage.GetVehicle(ctx, tx, vehicleID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, apierr.Newf(apierr.CodeNotFound, "vehicle %q not found", vehicleID)
			}
			return nil, err
		}
		if p.Cursor != "" {
			cp, _ := decodeCursor(p.Cursor)
			if cp.Revision != v.Revision {
				return nil, apierr.Newf(apierr.CodeRevisionConflict, "cursor revision %d does not match current revision %d", cp.Revision, v.Revision).
					WithDetails(map[string]any{"revision": v.Revision})
			}
		}
		if err := checkAtRevision(p.AtRevision, v.Revision); err != nil {
			return nil, err
		}

		rows, err := storage.ListCurrentTripsAfter(ctx, tx, vehicleID, p.IncludeMerged, p.Status, afterStartMs, afterTripID, limit+1)
		if err != nil {
			return nil, err
		}

		var items []TripSummary
		n := len(rows)
		if n > limit {
			n = limit
		}
		for _, t := range rows[:n] {
			items = append(items, tripSummaryFromRow(t))
		}

		var nextCursor *string
		if len(rows) > limit {
			last := rows[limit-1]
			c, err := encodeCursor(cursorPayload{Kind: "trips", VehicleID: vehicleID, Revision: v.Revision, LastStartMs: last.StartMs, LastTripID: last.TripID})
			if err != nil {
				return nil, err
			}
			nextCursor = &c
		}

		return &ListEnvelope[TripSummary]{Items: items, NextCursor: nextCursor, Revision: v.Revision, DatabaseID: s.Store.DatabaseID}, nil
	})
}

// ListFixesParams holds GET /api/v1/vehicles/{id}/fixes query parameters.
type ListFixesParams struct {
	FromMs     *int64
	ToMs       *int64
	Assignment string // "assigned" | "quarantined" | "all" (default)
	Limit      int
	Cursor     string
	AtRevision *int64
}

// ListVehicleFixes implements GET /api/v1/vehicles/{id}/fixes (AG-07).
func (s *Service) ListVehicleFixes(ctx context.Context, vehicleID string, p ListFixesParams) (*ListEnvelope[FixDTO], error) {
	limit := clampLimit(p.Limit)

	var afterMs *int64
	if p.Cursor != "" {
		cp, err := decodeCursor(p.Cursor)
		if err != nil {
			return nil, apierr.Newf(apierr.CodeInvalidRequest, "%v", err)
		}
		if cp.Kind != "fixes" || cp.VehicleID != vehicleID {
			return nil, apierr.New(apierr.CodeInvalidRequest, "cursor does not match this vehicle/fixes query")
		}
		afterMs = &cp.LastTimestampMs
	}

	return storage.ReadTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*ListEnvelope[FixDTO], error) {
		v, err := storage.GetVehicle(ctx, tx, vehicleID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, apierr.Newf(apierr.CodeNotFound, "vehicle %q not found", vehicleID)
			}
			return nil, err
		}
		if p.Cursor != "" {
			cp, _ := decodeCursor(p.Cursor)
			if cp.Revision != v.Revision {
				return nil, apierr.Newf(apierr.CodeRevisionConflict, "cursor revision %d does not match current revision %d", cp.Revision, v.Revision).
					WithDetails(map[string]any{"revision": v.Revision})
			}
		}
		if err := checkAtRevision(p.AtRevision, v.Revision); err != nil {
			return nil, err
		}

		rows, err := storage.ListFixesWithCurrentTripByVehicle(ctx, tx, vehicleID, p.FromMs, p.ToMs, afterMs, limit+1)
		if err != nil {
			return nil, err
		}

		var filtered []storage.FixWithCurrentTrip
		for _, r := range rows {
			switch p.Assignment {
			case "assigned":
				if r.Fix.AssignmentReason == nil && r.CurrentTripID != nil {
					filtered = append(filtered, r)
				}
			case "quarantined":
				if r.Fix.AssignmentReason != nil {
					filtered = append(filtered, r)
				}
			default:
				filtered = append(filtered, r)
			}
		}

		n := len(filtered)
		if n > limit {
			n = limit
		}
		var items []FixDTO
		for _, r := range filtered[:n] {
			items = append(items, fixDTOFromRow(r.Fix, r.CurrentTripID))
		}

		var nextCursor *string
		if len(rows) > limit {
			last := rows[limit-1]
			c, err := encodeCursor(cursorPayload{Kind: "fixes", VehicleID: vehicleID, Revision: v.Revision, LastTimestampMs: last.Fix.TimestampMs})
			if err != nil {
				return nil, err
			}
			nextCursor = &c
		}

		return &ListEnvelope[FixDTO]{Items: items, NextCursor: nextCursor, Revision: v.Revision, DatabaseID: s.Store.DatabaseID}, nil
	})
}

// GetTripDetail implements GET /api/v1/trips/{id} (AG-07): frozen or current
// metadata, close/merge details, immediate predecessors, immediate
// successor and resolved terminal successor.
func (s *Service) GetTripDetail(ctx context.Context, tripID string, atRevision *int64) (*TripDetail, error) {
	return storage.ReadTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*TripDetail, error) {
		t, err := storage.GetTrip(ctx, tx, tripID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, apierr.Newf(apierr.CodeNotFound, "trip %q not found", tripID)
			}
			return nil, err
		}
		vehicle, err := storage.GetVehicle(ctx, tx, t.VehicleID)
		if err != nil {
			return nil, err
		}
		if err := checkAtRevision(atRevision, vehicle.Revision); err != nil {
			return nil, err
		}

		preds, err := storage.ListImmediatePredecessors(ctx, tx, tripID)
		if err != nil {
			return nil, err
		}

		detail := &TripDetail{
			TripSummary:                 tripSummaryFromRow(*t),
			ImmediatePredecessorTripIDs: preds,
		}

		if succ, err := storage.GetImmediateSuccessor(ctx, tx, tripID); err == nil {
			detail.ImmediateSuccessorTripID = &succ
			terminal, err := storage.ResolveTerminalSuccessor(ctx, tx, tripID)
			if err != nil {
				return nil, err
			}
			detail.TerminalSuccessorTripID = &terminal
		} else if !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		}

		timestamps, err := storage.ListTripFixTimestamps(ctx, tx, tripID)
		if err != nil {
			return nil, err
		}
		c := trips.AnalyzeCompleteness(timestamps, s.Store.ExpectedFixIntervalMs)
		detail.Completeness = toCompletenessDTO(tripID, vehicle.Revision, s.Store.ExpectedFixIntervalMs, c)

		return detail, nil
	})
}

// GetTripFixes implements GET /api/v1/trips/{id}/fixes (AG-07): that trip's
// chronological snapshot geometry, including MERGED predecessor geometry
// (trip_fixes membership is frozen for closed trips, so this always
// reflects the trip's own snapshot regardless of current status).
func (s *Service) GetTripFixes(ctx context.Context, tripID string, atRevision *int64) (*ListEnvelope[FixDTO], error) {
	return storage.ReadTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*ListEnvelope[FixDTO], error) {
		t, err := storage.GetTrip(ctx, tx, tripID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, apierr.Newf(apierr.CodeNotFound, "trip %q not found", tripID)
			}
			return nil, err
		}
		vehicle, err := storage.GetVehicle(ctx, tx, t.VehicleID)
		if err != nil {
			return nil, err
		}
		if err := checkAtRevision(atRevision, vehicle.Revision); err != nil {
			return nil, err
		}

		fixRows, err := storage.ListTripFixes(ctx, tx, tripID)
		if err != nil {
			return nil, err
		}
		var items []FixDTO
		for _, f := range fixRows {
			tripIDCopy := tripID
			var currentTripID *string
			if t.CloseReason != storage.CloseReasonMerged {
				currentTripID = &tripIDCopy
			}
			items = append(items, fixDTOFromRow(f, currentTripID))
		}

		return &ListEnvelope[FixDTO]{Items: items, NextCursor: nil, Revision: vehicle.Revision, DatabaseID: s.Store.DatabaseID}, nil
	})
}

// HealthStatus is the response for GET /api/v1/health (AG-07).
type HealthStatus struct {
	Healthy        bool   `json:"healthy"`
	DatabaseID     string `json:"database_id"`
	TripGapSeconds int64  `json:"trip_gap_seconds"`
}

// Health implements GET /api/v1/health: 200 if migrations complete, DB
// usable and writer operational; otherwise the caller should respond 503.
func (s *Service) Health(ctx context.Context) (*HealthStatus, error) {
	if err := s.Store.Ping(ctx); err != nil {
		return nil, fmt.Errorf("aggregator: health check: %w", err)
	}
	return &HealthStatus{Healthy: true, DatabaseID: s.Store.DatabaseID, TripGapSeconds: s.Store.TripGapSeconds}, nil
}
