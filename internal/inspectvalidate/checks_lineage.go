package inspectvalidate

import "gpsdemo/internal/inspectclient"

// CheckLineage implements V07 LINEAGE: MERGED trips have exactly one
// immediate successor, same vehicle, and matching predecessor lists; the
// graph is acyclic; the resolved terminal successor is correct; every
// predecessor snapshot is a subset of its immediate successor geometry with
// identical measurements; and the terminal successor is never CLOSED/MERGED
// itself.
func CheckLineage(obs *Observation) Check {
	b := newCheckBuilder("V07")

	byID := map[string]inspectclient.TripDetail{}
	for _, t := range obs.Trips {
		byID[t.TripID] = t
	}

	for _, t := range obs.Trips {
		if t.CloseReason != inspectclient.CloseReasonMerged {
			continue
		}
		if t.ImmediateSuccessorTripID == nil {
			b.violate("MERGED trip %s has no immediate successor", t.TripID)
			continue
		}
		succ, ok := byID[*t.ImmediateSuccessorTripID]
		if !ok {
			b.violate("MERGED trip %s's successor %s was not observed", t.TripID, *t.ImmediateSuccessorTripID)
			continue
		}
		if succ.VehicleID != t.VehicleID {
			b.violate("MERGED trip %s's successor %s belongs to a different vehicle", t.TripID, succ.TripID)
		}

		// Matching predecessor lists: the successor's predecessor list must
		// include this trip.
		found := false
		for _, p := range succ.ImmediatePredecessorTripIDs {
			if p == t.TripID {
				found = true
				break
			}
		}
		if !found {
			b.violate("trip %s's successor %s does not list it as an immediate predecessor", t.TripID, succ.TripID)
		}

		// Predecessor snapshot is a subset of successor geometry with
		// identical measurements.
		succGeomByTs := map[int64]inspectclient.FixDTO{}
		for _, f := range obs.Geometry[succ.TripID] {
			succGeomByTs[f.TimestampMs] = f
		}
		for _, f := range obs.Geometry[t.TripID] {
			sf, ok := succGeomByTs[f.TimestampMs]
			if !ok {
				b.violate("predecessor %s fix ts=%d is missing from successor %s's geometry", t.TripID, f.TimestampMs, succ.TripID)
				continue
			}
			if !measurementsEqual(f, sf) {
				b.violate("predecessor %s fix ts=%d measurements differ from successor %s's copy", t.TripID, f.TimestampMs, succ.TripID)
			}
		}

		// SupersededByTripID (TripSummary) is documented as the immediate
		// successor; cross-check consistency between the two fields reported
		// by the API for the same trip.
		if t.SupersededByTripID != nil && *t.SupersededByTripID != *t.ImmediateSuccessorTripID {
			b.violate("trip %s superseded_by_trip_id (%s) disagrees with immediate_successor_trip_id (%s)", t.TripID, *t.SupersededByTripID, *t.ImmediateSuccessorTripID)
		}
	}

	// Acyclicity: follow immediate-successor edges from every trip and
	// detect a repeated visit.
	for _, t := range obs.Trips {
		visited := map[string]bool{t.TripID: true}
		cur := t
		for cur.ImmediateSuccessorTripID != nil {
			next, ok := byID[*cur.ImmediateSuccessorTripID]
			if !ok {
				break
			}
			if visited[next.TripID] {
				b.violate("lineage cycle detected starting from trip %s", t.TripID)
				break
			}
			visited[next.TripID] = true
			cur = next
		}
	}

	// Resolved terminal successor: following immediate-successor edges to a
	// fixed point must equal TerminalSuccessorTripID when it is non-nil, and
	// the terminal trip must never itself be CLOSED/MERGED.
	for _, t := range obs.Trips {
		if t.ImmediateSuccessorTripID == nil {
			continue
		}
		cur := t
		seen := map[string]bool{t.TripID: true}
		for cur.ImmediateSuccessorTripID != nil {
			next, ok := byID[*cur.ImmediateSuccessorTripID]
			if !ok {
				b.violate("trip %s's lineage chain references unobserved trip %s", t.TripID, *cur.ImmediateSuccessorTripID)
				break
			}
			if seen[next.TripID] {
				break // already reported as a cycle above
			}
			seen[next.TripID] = true
			cur = next
		}
		if t.TerminalSuccessorTripID == nil {
			b.violate("trip %s has an immediate successor but no terminal_successor_trip_id", t.TripID)
			continue
		}
		if *t.TerminalSuccessorTripID != cur.TripID {
			b.violate("trip %s terminal_successor_trip_id=%s does not match independently resolved terminal %s", t.TripID, *t.TerminalSuccessorTripID, cur.TripID)
		}
		if cur.CloseReason == inspectclient.CloseReasonMerged {
			b.violate("trip %s's resolved terminal successor %s is itself CLOSED/MERGED", t.TripID, cur.TripID)
		}
	}

	return b.result("MERGED lineage is acyclic, consistent, geometry-subset-preserving, and terminal successors resolve correctly")
}
