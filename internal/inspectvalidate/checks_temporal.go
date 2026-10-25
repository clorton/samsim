package inspectvalidate

import "gpsdemo/internal/inspectclient"

// CheckTemporalComponents implements V04 TEMPORAL_COMPONENTS: ACTIVE/
// INACTIVE memberships equal independently computed maximal components
// under gap<=configured threshold and manual barriers; no split/extra
// component or crossing barrier.
func CheckTemporalComponents(obs *Observation) Check {
	b := newCheckBuilder("V04")

	manualIntervals := deriveManualIntervals(obs)
	expectedComponents := computeExpectedComponents(obs, manualIntervals)

	var expectedSets []timestampSet
	for _, c := range expectedComponents {
		expectedSets = append(expectedSets, toTimestampSet(c.timestamps))
	}

	var actualSets []timestampSet
	var actualTripIDs []string
	for _, t := range obs.Trips {
		if t.Status != inspectclient.TripStatusActive && t.Status != inspectclient.TripStatusInactive {
			continue
		}
		actualSets = append(actualSets, fixesToTimestampSet(obs.Geometry[t.TripID]))
		actualTripIDs = append(actualTripIDs, t.TripID)
	}

	if len(actualSets) != len(expectedSets) {
		b.violate("found %d mutable (ACTIVE/INACTIVE) trips but independently computed %d expected temporal components", len(actualSets), len(expectedSets))
	}

	matched := make([]bool, len(expectedSets))
	for i, actual := range actualSets {
		found := false
		for j, expected := range expectedSets {
			if matched[j] {
				continue
			}
			if actual.equals(expected) {
				matched[j] = true
				found = true
				break
			}
		}
		if !found {
			b.violate("mutable trip %s's membership does not match any independently computed temporal component", actualTripIDs[i])
		}
	}

	return b.result("mutable trip memberships exactly match independently computed maximal temporal components")
}
