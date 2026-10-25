package inspectvalidate

// CheckCompleteness implements V08 COMPLETENESS: independently compute
// max(0,ceil(delta/I)-1) per adjacent fix pair in each trip's geometry;
// aggregate counts/intervals must match the completeness DTO, complete/
// assessment must agree, and boundary status must be UNKNOWN. Does not
// enumerate every expected missing point, only aggregate counts/intervals.
func CheckCompleteness(obs *Observation) Check {
	b := newCheckBuilder("V08")

	for _, t := range obs.Trips {
		if t.Completeness == nil {
			b.violate("trip %s has no completeness detail", t.TripID)
			continue
		}
		c := t.Completeness
		geom := obs.Geometry[t.TripID]

		var missingCount int64
		var intervalCount int
		interval := obs.ExpectedFixIntervalMs
		if interval > 0 {
			for i := 1; i < len(geom); i++ {
				a, bb := geom[i-1].TimestampMs, geom[i].TimestampMs
				delta := bb - a
				if delta <= 0 {
					continue
				}
				quotient := delta / interval
				remainder := delta % interval
				ceilDiv := quotient
				if remainder != 0 {
					ceilDiv++
				}
				missing := ceilDiv - 1
				if missing > 0 {
					missingCount += missing
					intervalCount++
				}
			}
		}

		if missingCount != c.MissingFixCount {
			b.violate("trip %s independently computed missing_fix_count=%d, API reported %d", t.TripID, missingCount, c.MissingFixCount)
		}
		if intervalCount != len(c.MissingIntervals) {
			b.violate("trip %s independently computed %d missing intervals, API reported %d", t.TripID, intervalCount, len(c.MissingIntervals))
		}

		expectComplete := missingCount == 0
		if expectComplete != c.Complete {
			b.violate("trip %s independently computed complete=%v, API reported %v", t.TripID, expectComplete, c.Complete)
		}
		expectedAssessment := "INTERNAL_GAPS"
		if expectComplete {
			expectedAssessment = "NO_INTERNAL_GAPS"
		}
		if c.Assessment != expectedAssessment {
			b.violate("trip %s assessment=%q does not agree with complete=%v", t.TripID, c.Assessment, c.Complete)
		}
		if c.BoundaryCompleteness != "UNKNOWN" {
			b.violate("trip %s boundary_completeness=%q, expected UNKNOWN", t.TripID, c.BoundaryCompleteness)
		}
	}

	return b.result("independently computed completeness aggregates match the API's completeness DTO for every trip")
}
