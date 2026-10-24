package aggregator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"gpsdemo/internal/apierr"
	"gpsdemo/internal/events"
	"gpsdemo/internal/httpapi"
	"gpsdemo/internal/storage"
)

// ingestTxResult is the value threaded from the ingestion transaction
// function to the post-commit publish step (AG-01's writer-coordinator
// publish discipline).
type ingestTxResult struct {
	response *IngestResult
	event    *events.Envelope // nil if no state changed
}

// Ingest runs the full AG-04 ingestion pipeline for an already decoded and
// semantically validated packet: fingerprinting, idempotency checks,
// per-vehicle+writer-coordinator locking, fix storage, AG-05 trip
// reconstruction, revision bump, and post-commit event publication.
func (s *Service) Ingest(ctx context.Context, np *httpapi.NormalizedPacket) (*IngestResult, error) {
	fp, err := np.Fingerprint()
	if err != nil {
		return nil, fmt.Errorf("aggregator: fingerprint packet: %w", err)
	}
	fpHex := storage.FingerprintHex(fp)

	unlock := s.lockVehicle(np.VehicleID)
	defer unlock()

	result, err := storage.WithWriteTx(ctx, s.Store, func(ctx context.Context, tx *sql.Tx) (*ingestTxResult, error) {
		return s.ingestTx(ctx, tx, np, fpHex)
	}, func(r *ingestTxResult) {
		if r.event != nil {
			s.Hub.Publish(*r.event)
		}
	})
	if err != nil {
		return nil, err
	}
	return result.response, nil
}

func (s *Service) ingestTx(ctx context.Context, tx *sql.Tx, np *httpapi.NormalizedPacket, fpHex string) (*ingestTxResult, error) {
	nowMs := s.Clock.NowMs()

	// Step 3: global packet-key idempotency lookup.
	existingPkt, err := storage.GetPacket(ctx, tx, np.SimulatorID, np.PacketID)
	if err == nil {
		if existingPkt.CanonicalSHA256 == fpHex && existingPkt.VehicleID == np.VehicleID {
			var resp IngestResult
			if jsonErr := json.Unmarshal([]byte(existingPkt.StoredResultJSON), &resp); jsonErr != nil {
				return nil, fmt.Errorf("aggregator: parse stored result for %s/%s: %w", np.SimulatorID, np.PacketID, jsonErr)
			}
			resp.DuplicatePacket = true
			return &ingestTxResult{response: &resp, event: nil}, nil
		}
		return nil, apierr.Newf(apierr.CodePacketConflict,
			"packet %s/%s already exists with different content", np.SimulatorID, np.PacketID)
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}

	// Step 4: ensure vehicle, compare fixes against stored values.
	vehicle, err := storage.EnsureVehicle(ctx, tx, np.VehicleID, nowMs)
	if err != nil {
		return nil, err
	}

	timestamps := make([]int64, len(np.Fixes))
	for i, f := range np.Fixes {
		timestamps[i] = f.TimestampMs
	}
	storedExisting, err := storage.GetFixesByTimestamps(ctx, tx, np.VehicleID, timestamps)
	if err != nil {
		return nil, err
	}

	var newFixes []httpapi.NormalizedFix
	duplicateFixes := np.InPacketDuplicateFixes
	for _, f := range np.Fixes {
		if ex, ok := storedExisting[f.TimestampMs]; ok {
			if fixEqualsStored(f, ex) {
				duplicateFixes++
				continue
			}
			return nil, apierr.Newf(apierr.CodeFixConflict,
				"conflicting fix at vehicle_id=%s timestamp_ms=%d", np.VehicleID, f.TimestampMs).
				WithDetails(map[string]any{"vehicle_id": np.VehicleID, "timestamp_ms": f.TimestampMs})
		}
		newFixes = append(newFixes, f)
	}

	barriers, err := storage.ListManualBarriersByVehicle(ctx, tx, np.VehicleID)
	if err != nil {
		return nil, err
	}

	quarantinedCount := 0
	var eligibleNewFixes []httpapi.NormalizedFix
	for _, f := range newFixes {
		reason := assignmentReasonFor(f.TimestampMs, barriers)
		if err := storage.InsertFix(ctx, tx, storage.FixRow{
			VehicleID:        np.VehicleID,
			TimestampMs:      f.TimestampMs,
			Latitude:         f.Latitude,
			Longitude:        f.Longitude,
			SpeedMps:         float64(f.SpeedMps),
			HeadingDegrees:   float64(f.HeadingDegrees),
			FirstSimulatorID: np.SimulatorID,
			FirstPacketID:    np.PacketID,
			ReceivedAtMs:     nowMs,
			AssignmentReason: reason,
		}); err != nil {
			return nil, err
		}
		if reason != nil {
			quarantinedCount++
		} else {
			eligibleNewFixes = append(eligibleNewFixes, f)
		}
	}

	acceptedFixes := len(newFixes)

	resp := &IngestResult{
		SimulatorID:      np.SimulatorID,
		PacketID:         np.PacketID,
		DuplicatePacket:  false,
		AcceptedFixes:    acceptedFixes,
		DuplicateFixes:   duplicateFixes,
		QuarantinedFixes: quarantinedCount,
		TripUpdates:      0,
		Revision:         vehicle.Revision,
	}

	var env *events.Envelope

	if acceptedFixes > 0 {
		newRevision := vehicle.Revision + 1
		outcome, err := s.reconstructTrips(ctx, tx, np.VehicleID, barriers, newRevision, nowMs, eligibleNewFixes)
		if err != nil {
			return nil, err
		}
		resp.TripUpdates = outcome.tripUpdates
		resp.Revision = newRevision

		if quarantinedCount > 0 {
			outcome.changes = append(outcome.changes, events.Change{
				Type:             events.ChangeTelemetryQuarantine,
				QuarantinedCount: quarantinedCount,
			})
		}

		newVehicle := storage.VehicleRow{
			VehicleID:            np.VehicleID,
			LatestFixTimestampMs: &outcome.latestFixTimestampMs,
			ActiveTripID:         outcome.activeTripID,
			Revision:             newRevision,
			UpdatedAtMs:          nowMs,
		}
		if err := storage.UpdateVehicle(ctx, tx, newVehicle); err != nil {
			return nil, err
		}

		outcome.changes = append(outcome.changes, events.Change{
			Type:              events.ChangeVehicleUpdated,
			ActiveTripID:      outcome.activeTripID,
			LatestTimestampMs: &outcome.latestFixTimestampMs,
		})

		env = &events.Envelope{
			EventID:     fmt.Sprintf("%s:%s:%d", s.Store.DatabaseID, np.VehicleID, newRevision),
			DatabaseID:  s.Store.DatabaseID,
			VehicleID:   np.VehicleID,
			Revision:    newRevision,
			Type:        "vehicle.changed",
			TimestampMs: nowMs,
			Changes:     outcome.changes,
		}
	}

	resultJSON, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("aggregator: marshal ingest result: %w", err)
	}
	if err := storage.InsertPacket(ctx, tx, storage.PacketRow{
		SimulatorID:      np.SimulatorID,
		PacketID:         np.PacketID,
		VehicleID:        np.VehicleID,
		CanonicalSHA256:  fpHex,
		ReceivedAtMs:     nowMs,
		AcceptedRevision: resp.Revision,
		StoredResultJSON: string(resultJSON),
	}); err != nil {
		return nil, err
	}

	return &ingestTxResult{response: resp, event: env}, nil
}

func fixEqualsStored(f httpapi.NormalizedFix, ex storage.FixRow) bool {
	return f.Latitude == ex.Latitude &&
		f.Longitude == ex.Longitude &&
		float64(f.SpeedMps) == ex.SpeedMps &&
		float64(f.HeadingDegrees) == ex.HeadingDegrees
}

func assignmentReasonFor(timestampMs int64, barriers []storage.ManualBarrierRow) *string {
	for _, b := range barriers {
		if timestampMs >= b.StartMs && timestampMs <= b.EndMs {
			reason := storage.AssignmentReasonManualClosedInterval
			return &reason
		}
	}
	return nil
}
