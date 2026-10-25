package inspectvalidate

import "gpsdemo/internal/inspectclient"

var validStatuses = map[string]bool{
	inspectclient.TripStatusActive:   true,
	inspectclient.TripStatusInactive: true,
	inspectclient.TripStatusClosed:   true,
}

var validCloseReasons = map[string]bool{
	inspectclient.CloseReasonNone:   true,
	inspectclient.CloseReasonManual: true,
	inspectclient.CloseReasonMerged: true,
}

// CheckTripMetadata implements V03 TRIP_METADATA: every trip's geometry is
// ordered/unique/nonempty, its count/first/last timestamps and coordinate
// extrema match its summary, status/reason/successor combinations and
// vehicle IDs are valid, and created_revision <= updated_revision <= R.
func CheckTripMetadata(obs *Observation) Check {
	b := newCheckBuilder("V03")

	for _, t := range obs.Trips {
		if t.VehicleID != obs.VehicleID {
			b.violate("trip %s has vehicle_id %q, expected %q", t.TripID, t.VehicleID, obs.VehicleID)
		}
		if !validStatuses[t.Status] {
			b.violate("trip %s has invalid status %q", t.TripID, t.Status)
		}
		if !validCloseReasons[t.CloseReason] {
			b.violate("trip %s has invalid close_reason %q", t.TripID, t.CloseReason)
		}
		if t.Status == inspectclient.TripStatusClosed && t.CloseReason == inspectclient.CloseReasonNone {
			b.violate("trip %s is CLOSED but close_reason is NONE", t.TripID)
		}
		if t.Status != inspectclient.TripStatusClosed && t.CloseReason != inspectclient.CloseReasonNone {
			b.violate("trip %s is not CLOSED but close_reason is %q", t.TripID, t.CloseReason)
		}
		if t.CloseReason == inspectclient.CloseReasonMerged && t.SupersededByTripID == nil {
			b.violate("trip %s is CLOSED/MERGED but has no superseded_by_trip_id", t.TripID)
		}
		if t.CloseReason != inspectclient.CloseReasonMerged && t.SupersededByTripID != nil {
			b.violate("trip %s has superseded_by_trip_id set but close_reason is %q, not MERGED", t.TripID, t.CloseReason)
		}
		if t.CreatedRevision > t.UpdatedRevision {
			b.violate("trip %s created_revision %d > updated_revision %d", t.TripID, t.CreatedRevision, t.UpdatedRevision)
		}
		if t.UpdatedRevision > obs.Revision {
			b.violate("trip %s updated_revision %d exceeds observation revision %d", t.TripID, t.UpdatedRevision, obs.Revision)
		}

		geom := obs.Geometry[t.TripID]
		if len(geom) == 0 {
			b.violate("trip %s has empty geometry", t.TripID)
			continue
		}
		if int64(len(geom)) != t.FixCount {
			b.violate("trip %s fix_count=%d but geometry has %d fixes", t.TripID, t.FixCount, len(geom))
		}

		seen := map[int64]bool{}
		var prevTs int64
		minLat, maxLat := geom[0].Latitude, geom[0].Latitude
		minLon, maxLon := geom[0].Longitude, geom[0].Longitude
		for i, f := range geom {
			if seen[f.TimestampMs] {
				b.violate("trip %s has duplicate geometry timestamp %d", t.TripID, f.TimestampMs)
			}
			seen[f.TimestampMs] = true
			if i > 0 && f.TimestampMs <= prevTs {
				b.violate("trip %s geometry not strictly ordered at ts=%d", t.TripID, f.TimestampMs)
			}
			prevTs = f.TimestampMs
			if f.Latitude < minLat {
				minLat = f.Latitude
			}
			if f.Latitude > maxLat {
				maxLat = f.Latitude
			}
			if f.Longitude < minLon {
				minLon = f.Longitude
			}
			if f.Longitude > maxLon {
				maxLon = f.Longitude
			}
		}
		if geom[0].TimestampMs != t.StartMs {
			b.violate("trip %s start_ms=%d does not match first geometry fix ts=%d", t.TripID, t.StartMs, geom[0].TimestampMs)
		}
		if geom[len(geom)-1].TimestampMs != t.EndMs {
			b.violate("trip %s end_ms=%d does not match last geometry fix ts=%d", t.TripID, t.EndMs, geom[len(geom)-1].TimestampMs)
		}
		if minLat != t.Bounds.MinLatitude || maxLat != t.Bounds.MaxLatitude || minLon != t.Bounds.MinLongitude || maxLon != t.Bounds.MaxLongitude {
			b.violate("trip %s bounds do not match geometry extrema", t.TripID)
		}
	}

	return b.result("all trip geometries are ordered/unique/nonempty and match their summary metadata")
}

// CheckActivity implements V06 ACTIVITY: at most one ACTIVE trip; the
// active pointer/status agree; exactly the mutable trip containing the
// maximum raw timestamp is active, otherwise null; an older mutable trip is
// not activated if the latest fix belongs to a manual snapshot/quarantine.
func CheckActivity(obs *Observation) Check {
	b := newCheckBuilder("V06")

	var activeTrips []string
	for _, t := range obs.Trips {
		if t.Status == inspectclient.TripStatusActive {
			activeTrips = append(activeTrips, t.TripID)
		}
	}
	if len(activeTrips) > 1 {
		b.violate("more than one ACTIVE trip observed: %v", activeTrips)
	}

	var activePointer *string
	if len(activeTrips) == 1 {
		activePointer = &activeTrips[0]
	}

	if len(obs.RawFixes) == 0 {
		if activePointer != nil {
			b.violate("expected no active trip (no raw fixes observed), but %s is ACTIVE", *activePointer)
		}
		return b.result("at most one ACTIVE trip, and it is exactly the mutable trip containing the overall maximum raw timestamp")
	}

	// Find the overall maximum raw timestamp fix (regardless of eligibility):
	// per spec, "An older mutable trip is not activated if latest fix belongs
	// to manual snapshot/quarantine" -- i.e. if the single latest raw fix is
	// quarantined or inside a manual-closed interval, no trip should be
	// ACTIVE at all (no falling back to an older mutable trip).
	maxFix := obs.RawFixes[0]
	for _, f := range obs.RawFixes {
		if f.TimestampMs > maxFix.TimestampMs {
			maxFix = f
		}
	}

	manualIntervals := deriveManualIntervals(obs)
	latestIsManualOrQuarantined := maxFix.AssignmentReason != nil
	if !latestIsManualOrQuarantined {
		for _, iv := range manualIntervals {
			if iv.contains(maxFix.TimestampMs) {
				latestIsManualOrQuarantined = true
				break
			}
		}
	}

	var expectedActive *string
	if !latestIsManualOrQuarantined {
		for _, t := range obs.Trips {
			if t.Status != inspectclient.TripStatusActive && t.Status != inspectclient.TripStatusInactive {
				continue
			}
			for _, f := range obs.Geometry[t.TripID] {
				if f.TimestampMs == maxFix.TimestampMs {
					id := t.TripID
					expectedActive = &id
					break
				}
			}
		}
	}

	switch {
	case expectedActive == nil && activePointer != nil:
		b.violate("expected no active trip (the latest raw fix is quarantined or within a manual-closed interval), but %s is ACTIVE", *activePointer)
	case expectedActive != nil && activePointer == nil:
		b.violate("expected trip %s to be ACTIVE (contains the maximum raw timestamp), but no trip is ACTIVE", *expectedActive)
	case expectedActive != nil && activePointer != nil && *expectedActive != *activePointer:
		b.violate("expected trip %s to be ACTIVE, but %s is ACTIVE instead", *expectedActive, *activePointer)
	}

	return b.result("at most one ACTIVE trip, and it is exactly the mutable trip containing the maximum eligible raw timestamp")
}
