package inspectvalidate

import "gpsdemo/internal/inspectclient"

// CheckRawIdentity implements V01 RAW_IDENTITY: raw fixes strictly increase
// in timestamp, belong to the requested vehicle, are unique, satisfy AG-02
// measurement bounds, and the latest position matches the max raw timestamp
// and all its measurements.
func CheckRawIdentity(obs *Observation) Check {
	b := newCheckBuilder("V01")
	seen := map[int64]bool{}
	var prevTs int64
	var maxTs int64
	var maxFix *inspectclient.FixDTO
	for i, f := range obs.RawFixes {
		if !isFinite(f) {
			b.violate("fix at index %d has a non-finite measurement", i)
			continue
		}
		if !withinMeasurementBounds(f) {
			b.violate("fix ts=%d out of AG-02 measurement bounds: lat=%v lon=%v speed=%v heading=%v", f.TimestampMs, f.Latitude, f.Longitude, f.SpeedMps, f.HeadingDegrees)
		}
		if seen[f.TimestampMs] {
			b.violate("duplicate raw fix timestamp %d", f.TimestampMs)
		}
		seen[f.TimestampMs] = true
		if i > 0 && f.TimestampMs <= prevTs {
			b.violate("raw fixes not strictly increasing: ts=%d follows ts=%d", f.TimestampMs, prevTs)
		}
		prevTs = f.TimestampMs
		if f.TimestampMs >= maxTs {
			maxTs = f.TimestampMs
			fCopy := f
			maxFix = &fCopy
		}
	}

	if obs.LatestFix == nil {
		if len(obs.RawFixes) > 0 {
			b.violate("vehicle has raw fixes but latest_fix is null")
		}
	} else if maxFix == nil {
		b.violate("vehicle reports a latest_fix but no raw fixes were observed")
	} else if !measurementsEqual(*obs.LatestFix, *maxFix) {
		b.violate("latest_fix (ts=%d) does not match the raw fix with the maximum timestamp (ts=%d)", obs.LatestFix.TimestampMs, maxFix.TimestampMs)
	}

	return b.result("raw fixes are unique, strictly ordered, within bounds, and latest_fix matches the max-timestamp raw fix")
}

// CheckCurrentMembership implements V02 CURRENT_MEMBERSHIP: each assigned
// raw fix appears exactly once among non-MERGED trips, current_trip_id
// agrees, quarantined fixes have no such membership, and every geometry
// member exists in raw history with matching values.
func CheckCurrentMembership(obs *Observation) Check {
	b := newCheckBuilder("V02")

	rawByTs := map[int64]inspectclient.FixDTO{}
	for _, f := range obs.RawFixes {
		rawByTs[f.TimestampMs] = f
	}

	// membershipCount counts, for each raw timestamp, how many non-MERGED
	// trips' geometry claim it.
	membershipCount := map[int64]int{}
	membershipTrip := map[int64]string{}

	for _, t := range obs.Trips {
		if t.CloseReason == inspectclient.CloseReasonMerged {
			continue // MERGED audit copies do not count as current assignments
		}
		for _, g := range obs.Geometry[t.TripID] {
			membershipCount[g.TimestampMs]++
			membershipTrip[g.TimestampMs] = t.TripID

			raw, ok := rawByTs[g.TimestampMs]
			if !ok {
				b.violate("trip %s geometry fix ts=%d has no matching raw fix", t.TripID, g.TimestampMs)
				continue
			}
			if !measurementsEqual(raw, g) {
				b.violate("trip %s geometry fix ts=%d does not match raw fix measurements", t.TripID, g.TimestampMs)
			}
		}
	}

	for _, f := range obs.RawFixes {
		quarantined := f.AssignmentReason != nil
		count := membershipCount[f.TimestampMs]
		if quarantined {
			if count != 0 {
				b.violate("quarantined fix ts=%d unexpectedly has current trip membership", f.TimestampMs)
			}
			if f.CurrentTripID != nil {
				b.violate("quarantined fix ts=%d has a non-null current_trip_id", f.TimestampMs)
			}
			continue
		}
		if count == 0 {
			b.violate("assigned fix ts=%d (not quarantined) has no current trip membership", f.TimestampMs)
			continue
		}
		if count > 1 {
			b.violate("fix ts=%d appears in %d non-MERGED trips simultaneously", f.TimestampMs, count)
		}
		if f.CurrentTripID == nil {
			b.violate("fix ts=%d has current trip membership but current_trip_id is null", f.TimestampMs)
		} else if *f.CurrentTripID != membershipTrip[f.TimestampMs] {
			b.violate("fix ts=%d current_trip_id=%s does not match actual membership trip %s", f.TimestampMs, *f.CurrentTripID, membershipTrip[f.TimestampMs])
		}
	}

	return b.result("every assigned fix has exactly one current-trip membership matching current_trip_id; quarantined fixes have none")
}
