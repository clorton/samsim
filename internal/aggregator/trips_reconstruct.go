package aggregator

import (
	"context"
	"database/sql"
	"fmt"

	"gpsdemo/internal/events"
	"gpsdemo/internal/httpapi"
	"gpsdemo/internal/storage"
	"gpsdemo/internal/trips"
)

// reconstructOutcome summarizes the effect of one AG-05 reconstruction pass,
// used to update the vehicle row and build the event envelope.
type reconstructOutcome struct {
	tripUpdates          int
	changes              []events.Change
	latestFixTimestampMs int64
	activeTripID         *string
}

// reconstructTrips implements AG-05: it recomputes all mutable components
// for the vehicle, reconciles them against pretransaction ACTIVE/INACTIVE
// trips by shared membership, persists only changed rows, and returns a
// summary for the vehicle update and event envelope. newNonQuarantinedFixes
// are the fixes newly inserted by this packet that are NOT inside any
// manual barrier (i.e. eligible for mutable reconstruction).
func (s *Service) reconstructTrips(
	ctx context.Context,
	tx *sql.Tx,
	vehicleID string,
	barrierRows []storage.ManualBarrierRow,
	newRevision int64,
	nowMs int64,
	newNonQuarantinedFixes []httpapi.NormalizedFix,
) (*reconstructOutcome, error) {
	existingTripRows, err := storage.ListMutableTripsByVehicle(ctx, tx, vehicleID)
	if err != nil {
		return nil, err
	}
	tripRowByID := make(map[string]storage.TripRow, len(existingTripRows))
	for _, t := range existingTripRows {
		tripRowByID[t.TripID] = t
	}

	var existing []trips.ExistingTrip
	var eligible []int64
	for _, t := range existingTripRows {
		members, err := storage.ListTripFixTimestamps(ctx, tx, t.TripID)
		if err != nil {
			return nil, err
		}
		existing = append(existing, trips.ExistingTrip{TripID: t.TripID, Status: t.Status, Members: members})
		eligible = append(eligible, members...)
	}

	newFixesByTs := make(map[int64]httpapi.NormalizedFix, len(newNonQuarantinedFixes))
	for _, f := range newNonQuarantinedFixes {
		newFixesByTs[f.TimestampMs] = f
		eligible = append(eligible, f.TimestampMs)
	}

	var barriers []trips.BarrierInterval
	for _, b := range barrierRows {
		barriers = append(barriers, trips.BarrierInterval{StartMs: b.StartMs, EndMs: b.EndMs})
	}

	globalMax, hasAny, err := storage.MaxFixTimestamp(ctx, tx, vehicleID)
	if err != nil {
		return nil, err
	}
	maxTs := int64(-1)
	if hasAny {
		maxTs = globalMax
	}

	result, err := trips.Reconcile(trips.Params{
		GapMs:              s.Store.TripGapSeconds * 1000,
		Barriers:           barriers,
		Existing:           existing,
		EligibleTimestamps: eligible,
		MaxFixTimestamp:    maxTs,
	})
	if err != nil {
		return nil, fmt.Errorf("aggregator: reconcile trips: %w", err)
	}

	outcome := &reconstructOutcome{latestFixTimestampMs: globalMax}

	componentTripID := make([]string, len(result.Components))

	for ci, comp := range result.Components {
		isActive := ci == result.ActiveComponentIndex
		newStatus := storage.TripStatusInactive
		if isActive {
			newStatus = storage.TripStatusActive
		}

		switch len(comp.Predecessors) {
		case 0:
			tripID := s.IDGen.NewID()
			componentTripID[ci] = tripID
			minLat, maxLat, minLon, maxLon, startMs, endMs := boundsFromNewFixes(comp.Timestamps, newFixesByTs)
			row := storage.TripRow{
				TripID: tripID, VehicleID: vehicleID, StartMs: startMs, EndMs: endMs,
				FixCount:    int64(len(comp.Timestamps)),
				MinLatitude: minLat, MaxLatitude: maxLat, MinLongitude: minLon, MaxLongitude: maxLon,
				Status: newStatus, CloseReason: storage.CloseReasonNone,
				CreatedRevision: newRevision, UpdatedRevision: newRevision,
			}
			if err := storage.InsertTrip(ctx, tx, row); err != nil {
				return nil, err
			}
			for _, ts := range comp.Timestamps {
				if err := storage.InsertTripFix(ctx, tx, tripID, vehicleID, ts); err != nil {
					return nil, err
				}
			}
			outcome.tripUpdates++
			outcome.changes = append(outcome.changes, events.Change{Type: events.ChangeTripCreated, TripID: tripID})

		case 1:
			oldID := comp.Predecessors[0]
			oldRow := tripRowByID[oldID]
			componentTripID[ci] = oldID

			oldMembers := map[int64]bool{}
			for _, et := range existing {
				if et.TripID == oldID {
					for _, m := range et.Members {
						oldMembers[m] = true
					}
					break
				}
			}
			var newMembers []int64
			for _, ts := range comp.Timestamps {
				if !oldMembers[ts] {
					newMembers = append(newMembers, ts)
				}
			}

			changed := len(newMembers) > 0 || oldRow.Status != newStatus
			if !changed {
				continue
			}

			minLat, maxLat, minLon, maxLon := oldRow.MinLatitude, oldRow.MaxLatitude, oldRow.MinLongitude, oldRow.MaxLongitude
			startMs, endMs := oldRow.StartMs, oldRow.EndMs
			for _, ts := range newMembers {
				f := newFixesByTs[ts]
				minLat, maxLat = minF64(minLat, float64(f.Latitude)), maxF64(maxLat, float64(f.Latitude))
				minLon, maxLon = minF64(minLon, float64(f.Longitude)), maxF64(maxLon, float64(f.Longitude))
				if ts < startMs {
					startMs = ts
				}
				if ts > endMs {
					endMs = ts
				}
				if err := storage.InsertTripFix(ctx, tx, oldID, vehicleID, ts); err != nil {
					return nil, err
				}
			}

			updatedRow := oldRow
			updatedRow.StartMs, updatedRow.EndMs = startMs, endMs
			updatedRow.FixCount = oldRow.FixCount + int64(len(newMembers))
			updatedRow.MinLatitude, updatedRow.MaxLatitude = minLat, maxLat
			updatedRow.MinLongitude, updatedRow.MaxLongitude = minLon, maxLon
			updatedRow.Status = newStatus
			updatedRow.UpdatedRevision = newRevision
			if err := storage.UpdateTrip(ctx, tx, updatedRow); err != nil {
				return nil, err
			}
			outcome.tripUpdates++
			outcome.changes = append(outcome.changes, events.Change{Type: events.ChangeTripUpdated, TripID: oldID})

		default:
			newID := s.IDGen.NewID()
			componentTripID[ci] = newID

			fixRows, err := storage.GetFixesByTimestamps(ctx, tx, vehicleID, comp.Timestamps)
			if err != nil {
				return nil, err
			}
			minLat, maxLat := float64(0), float64(0)
			minLon, maxLon := float64(0), float64(0)
			startMs, endMs := comp.Timestamps[0], comp.Timestamps[0]
			for i, ts := range comp.Timestamps {
				fr, ok := fixRows[ts]
				if !ok {
					return nil, fmt.Errorf("aggregator: missing fix row for timestamp %d during merge", ts)
				}
				if i == 0 {
					minLat, maxLat = fr.Latitude, fr.Latitude
					minLon, maxLon = fr.Longitude, fr.Longitude
				} else {
					minLat, maxLat = minF64(minLat, fr.Latitude), maxF64(maxLat, fr.Latitude)
					minLon, maxLon = minF64(minLon, fr.Longitude), maxF64(maxLon, fr.Longitude)
				}
				if ts < startMs {
					startMs = ts
				}
				if ts > endMs {
					endMs = ts
				}
			}

			// Insert the new successor trip first, always as INACTIVE: this
			// satisfies the predecessor rows' superseded_by_trip_id foreign key
			// (the referenced row must already exist) while never tripping the
			// partial unique ACTIVE index before the old ACTIVE predecessor (if
			// any) is closed below. If the component should end up ACTIVE, it is
			// promoted afterward.
			newTripRow := storage.TripRow{
				TripID: newID, VehicleID: vehicleID, StartMs: startMs, EndMs: endMs,
				FixCount:    int64(len(comp.Timestamps)),
				MinLatitude: minLat, MaxLatitude: maxLat, MinLongitude: minLon, MaxLongitude: maxLon,
				Status: storage.TripStatusInactive, CloseReason: storage.CloseReasonNone,
				CreatedRevision: newRevision, UpdatedRevision: newRevision,
			}
			if err := storage.InsertTrip(ctx, tx, newTripRow); err != nil {
				return nil, err
			}
			for _, ts := range comp.Timestamps {
				if err := storage.InsertTripFix(ctx, tx, newID, vehicleID, ts); err != nil {
					return nil, err
				}
			}

			for _, predID := range comp.Predecessors {
				predRow := tripRowByID[predID]
				predRow.Status = storage.TripStatusClosed
				predRow.CloseReason = storage.CloseReasonMerged
				predRow.SupersededByTripID = &newID
				predRow.UpdatedRevision = newRevision
				if err := storage.UpdateTrip(ctx, tx, predRow); err != nil {
					return nil, err
				}
				if err := storage.InsertTripMerge(ctx, tx, storage.TripMergeRow{
					PredecessorTripID: predID, SuccessorTripID: newID, VehicleID: vehicleID,
					Revision: newRevision, MergedAtMs: nowMs,
				}); err != nil {
					return nil, err
				}
			}

			if newStatus == storage.TripStatusActive {
				newTripRow.Status = storage.TripStatusActive
				if err := storage.UpdateTrip(ctx, tx, newTripRow); err != nil {
					return nil, err
				}
			}

			outcome.tripUpdates += 1 + len(comp.Predecessors)
			outcome.changes = append(outcome.changes, events.Change{
				Type:               events.ChangeTripsMerged,
				PredecessorTripIDs: append([]string(nil), comp.Predecessors...),
				SuccessorTripID:    newID,
			})
		}
	}

	if result.ActiveComponentIndex >= 0 {
		id := componentTripID[result.ActiveComponentIndex]
		outcome.activeTripID = &id
	}

	return outcome, nil
}

func boundsFromNewFixes(timestamps []int64, byTs map[int64]httpapi.NormalizedFix) (minLat, maxLat, minLon, maxLon float64, startMs, endMs int64) {
	startMs, endMs = timestamps[0], timestamps[0]
	for i, ts := range timestamps {
		f := byTs[ts]
		if i == 0 {
			minLat, maxLat = float64(f.Latitude), float64(f.Latitude)
			minLon, maxLon = float64(f.Longitude), float64(f.Longitude)
		} else {
			minLat, maxLat = minF64(minLat, float64(f.Latitude)), maxF64(maxLat, float64(f.Latitude))
			minLon, maxLon = minF64(minLon, float64(f.Longitude)), maxF64(maxLon, float64(f.Longitude))
		}
		if ts < startMs {
			startMs = ts
		}
		if ts > endMs {
			endMs = ts
		}
	}
	return
}

func minF64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
