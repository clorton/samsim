package inspectvalidate

import "gpsdemo/internal/inspectclient"

// CheckManualBarriers implements V05 MANUAL_BARRIERS: closed manual trip
// intervals equal frozen geometry endpoints, are disjoint, have no
// successor, and match the snapshot's manual_barriers records. All raw
// fixes inside an interval either belong to that manual snapshot or carry
// the MANUAL_CLOSED_INTERVAL quarantine reason; no quarantine occurs
// outside every manual interval. Closed manual geometry remains a connected
// component at the configured gap threshold.
func CheckManualBarriers(obs *Observation) Check {
	b := newCheckBuilder("V05")

	var manualTrips []inspectclient.TripDetail
	for _, t := range obs.Trips {
		if t.Status == inspectclient.TripStatusClosed && t.CloseReason == inspectclient.CloseReasonManual {
			manualTrips = append(manualTrips, t)
		}
	}

	// Interval equals frozen geometry endpoints; no successor.
	intervals := map[string]interval{}
	for _, t := range manualTrips {
		geom := obs.Geometry[t.TripID]
		if len(geom) == 0 {
			b.violate("manual trip %s has empty geometry", t.TripID)
			continue
		}
		if t.StartMs != geom[0].TimestampMs || t.EndMs != geom[len(geom)-1].TimestampMs {
			b.violate("manual trip %s interval [%d,%d] does not equal frozen geometry endpoints [%d,%d]", t.TripID, t.StartMs, t.EndMs, geom[0].TimestampMs, geom[len(geom)-1].TimestampMs)
		}
		if t.SupersededByTripID != nil {
			b.violate("manual trip %s has a successor, but manual closure must be terminal", t.TripID)
		}
		intervals[t.TripID] = interval{startMs: t.StartMs, endMs: t.EndMs}

		// Connected component at configured gap.
		gapThresholdMs := obs.TripGapSeconds * 1000
		for i := 1; i < len(geom); i++ {
			if geom[i].TimestampMs-geom[i-1].TimestampMs > gapThresholdMs {
				b.violate("manual trip %s geometry has an internal gap exceeding the configured threshold between ts=%d and ts=%d", t.TripID, geom[i-1].TimestampMs, geom[i].TimestampMs)
			}
		}
	}

	// Disjointness.
	ids := make([]string, 0, len(intervals))
	for id := range intervals {
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, c := intervals[ids[i]], intervals[ids[j]]
			if a.startMs <= c.endMs && c.startMs <= a.endMs {
				b.violate("manual intervals for trips %s and %s overlap", ids[i], ids[j])
			}
		}
	}

	// Match snapshot's manual_barriers records exactly (same set of
	// trip_id -> [start,end]).
	barrierByTrip := map[string]interval{}
	for _, mb := range obs.ManualBarriers {
		barrierByTrip[mb.TripID] = interval{startMs: mb.StartMs, endMs: mb.EndMs}
	}
	for id, iv := range intervals {
		bv, ok := barrierByTrip[id]
		if !ok {
			b.violate("manual trip %s has no corresponding manual_barriers record", id)
			continue
		}
		if bv != iv {
			b.violate("manual trip %s manual_barriers record [%d,%d] does not match trip interval [%d,%d]", id, bv.startMs, bv.endMs, iv.startMs, iv.endMs)
		}
	}
	for id := range barrierByTrip {
		if _, ok := intervals[id]; !ok {
			b.violate("manual_barriers record for trip %s has no corresponding CLOSED/MANUAL trip", id)
		}
	}

	// Raw fixes inside any interval: must belong to that manual snapshot's
	// own geometry, or be quarantined with MANUAL_CLOSED_INTERVAL. No
	// quarantine reason occurs outside every manual interval.
	manualMembership := map[int64]string{} // ts -> trip_id, for manual trips only
	for id := range intervals {
		for _, f := range obs.Geometry[id] {
			manualMembership[f.TimestampMs] = id
		}
	}
	for _, f := range obs.RawFixes {
		inAnyInterval := false
		var containingTrip string
		for id, iv := range intervals {
			if iv.contains(f.TimestampMs) {
				inAnyInterval = true
				containingTrip = id
				break
			}
		}
		if inAnyInterval {
			_, isMember := manualMembership[f.TimestampMs]
			isQuarantinedForThis := f.AssignmentReason != nil && *f.AssignmentReason == inspectclient.AssignmentReasonManualClosedInterval
			if !isMember && !isQuarantinedForThis {
				b.violate("raw fix ts=%d falls within manual interval %s but is neither a member of its snapshot nor quarantined as MANUAL_CLOSED_INTERVAL", f.TimestampMs, containingTrip)
			}
		} else {
			if f.AssignmentReason != nil && *f.AssignmentReason == inspectclient.AssignmentReasonManualClosedInterval {
				b.violate("raw fix ts=%d is quarantined as MANUAL_CLOSED_INTERVAL but falls outside every manual interval", f.TimestampMs)
			}
		}
	}

	return b.result("manual closure intervals are disjoint, terminal, match frozen geometry and snapshot barriers, and quarantine is scoped correctly")
}
