package inspectvalidate

import "gpsdemo/internal/inspectclient"

// CheckObservedImmutability implements V09 OBSERVED_IMMUTABILITY: for every
// trip already CLOSED in the baseline, its immutable metadata (identity/
// vehicle, geometry, bounds/count, status/reason, successor and closure
// audit fields, created/updated revisions) must be identical in the current
// observation; disappearance or change is a FAIL. Immediate predecessor
// edges must remain stable. The resolved terminal successor is explicitly
// excluded from this comparison (checked live by V07 instead). Trips that
// were previously ACTIVE/INACTIVE may grow/merge/close freely and are not
// frozen.
func CheckObservedImmutability(obs *Observation, baseline *Baseline) Check {
	if baseline == nil {
		return skip("V09", "no --baseline supplied; immutability across runs was not checked")
	}

	b := newCheckBuilder("V09")

	currentByID := map[string]inspectclient.TripDetail{}
	for _, t := range obs.Trips {
		currentByID[t.TripID] = t
	}
	currentGeom := obs.Geometry

	for _, bt := range baseline.Trips {
		if bt.Status != inspectclient.TripStatusClosed {
			continue // only CLOSED trips are frozen
		}
		ct, ok := currentByID[bt.TripID]
		if !ok {
			b.violate("baseline CLOSED trip %s has disappeared from the current observation", bt.TripID)
			continue
		}
		if ct.VehicleID != bt.VehicleID || ct.StartMs != bt.StartMs || ct.EndMs != bt.EndMs ||
			ct.FixCount != bt.FixCount || ct.Bounds != bt.Bounds || ct.Status != bt.Status ||
			ct.CloseReason != bt.CloseReason || !stringPtrEqual(ct.SupersededByTripID, bt.SupersededByTripID) ||
			ct.CreatedRevision != bt.CreatedRevision || ct.UpdatedRevision != bt.UpdatedRevision ||
			!int64PtrEqual(ct.ClosedAtMs, bt.ClosedAtMs) || ct.ForcedClose != bt.ForcedClose {
			b.violate("baseline CLOSED trip %s metadata changed in the current observation", bt.TripID)
		}
		if !stringSliceEqual(ct.ImmediatePredecessorTripIDs, bt.ImmediatePredecessorTripIDs) {
			b.violate("baseline CLOSED trip %s immediate predecessor edges changed", bt.TripID)
		}

		geom := currentGeom[bt.TripID]
		if len(geom) != len(bt.Geometry) {
			b.violate("baseline CLOSED trip %s geometry length changed: was %d, now %d", bt.TripID, len(bt.Geometry), len(geom))
			continue
		}
		for i := range geom {
			if !measurementsEqual(geom[i], bt.Geometry[i]) {
				b.violate("baseline CLOSED trip %s geometry fix at index %d changed", bt.TripID, i)
				break
			}
		}
	}

	return b.result("every trip already CLOSED in the baseline has identical frozen metadata and geometry now")
}

// CheckObservedMonotonicity implements V10 OBSERVED_MONOTONICITY: for the
// same database/vehicle baseline, revision never decreases and the latest
// event timestamp never regresses; an equal revision requires identical
// normalized captured state.
func CheckObservedMonotonicity(obs *Observation, baseline *Baseline) Check {
	if baseline == nil {
		return skip("V10", "no --baseline supplied; monotonicity across runs was not checked")
	}

	b := newCheckBuilder("V10")

	if baseline.DatabaseID != obs.DatabaseID {
		return warn("V10", "baseline database_id differs from the current observation; monotonicity is not meaningful across different databases")
	}

	if baseline.TripGapSeconds != obs.TripGapSeconds || baseline.ExpectedFixIntervalMs != obs.ExpectedFixIntervalMs {
		b.violate("segmentation config changed for the same database: trip_gap_seconds %d->%d, expected_fix_interval_ms %d->%d (AG-01 forbids silent config changes)",
			baseline.TripGapSeconds, obs.TripGapSeconds, baseline.ExpectedFixIntervalMs, obs.ExpectedFixIntervalMs)
	}

	if obs.Revision < baseline.Revision {
		b.violate("revision regressed: baseline had %d, current observation has %d", baseline.Revision, obs.Revision)
	}

	var baselineLatestTs, currentLatestTs int64
	if baseline.LatestFix != nil {
		baselineLatestTs = baseline.LatestFix.TimestampMs
	}
	if obs.LatestFix != nil {
		currentLatestTs = obs.LatestFix.TimestampMs
	}
	if currentLatestTs < baselineLatestTs {
		b.violate("latest event timestamp regressed: baseline had %d, current observation has %d", baselineLatestTs, currentLatestTs)
	}

	if obs.Revision == baseline.Revision {
		current := ToBaseline(obs)
		if !baselinesEqual(current, baseline) {
			b.violate("revision unchanged (%d) but normalized captured state differs from the baseline", obs.Revision)
		}
	}

	return b.result("revision and latest event timestamp are non-decreasing relative to the baseline")
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
	}
	for _, c := range seen {
		if c != 0 {
			return false
		}
	}
	return true
}

// baselinesEqual compares two normalized baselines field by field,
// excluding nothing (both are already-normalized domain state at this
// point); used only for the equal-revision identical-state check in V10.
func baselinesEqual(a, b *Baseline) bool {
	if a.DatabaseID != b.DatabaseID || a.VehicleID != b.VehicleID || a.Revision != b.Revision ||
		a.TripGapSeconds != b.TripGapSeconds || a.ExpectedFixIntervalMs != b.ExpectedFixIntervalMs {
		return false
	}
	if (a.LatestFix == nil) != (b.LatestFix == nil) {
		return false
	}
	if a.LatestFix != nil && !measurementsEqual(*a.LatestFix, *b.LatestFix) {
		return false
	}
	if len(a.Trips) != len(b.Trips) {
		return false
	}
	byID := map[string]BaselineTrip{}
	for _, t := range b.Trips {
		byID[t.TripID] = t
	}
	for _, t := range a.Trips {
		o, ok := byID[t.TripID]
		if !ok {
			return false
		}
		if t.VehicleID != o.VehicleID || t.StartMs != o.StartMs || t.EndMs != o.EndMs ||
			t.FixCount != o.FixCount || t.Bounds != o.Bounds || t.Status != o.Status ||
			t.CloseReason != o.CloseReason || !stringPtrEqual(t.SupersededByTripID, o.SupersededByTripID) ||
			t.CreatedRevision != o.CreatedRevision || t.UpdatedRevision != o.UpdatedRevision ||
			!int64PtrEqual(t.ClosedAtMs, o.ClosedAtMs) || t.ForcedClose != o.ForcedClose ||
			!stringSliceEqual(t.ImmediatePredecessorTripIDs, o.ImmediatePredecessorTripIDs) {
			return false
		}
		if len(t.Geometry) != len(o.Geometry) {
			return false
		}
		for i := range t.Geometry {
			if !measurementsEqual(t.Geometry[i], o.Geometry[i]) {
				return false
			}
		}
	}
	return true
}
