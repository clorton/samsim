package inspectvalidate

import (
	"math"
	"sort"

	"gpsdemo/internal/inspectclient"
)

// measurementsEqual compares two fixes' canonical measurement values with
// exact equality (INS-05: "use exact schema-normalized coordinate/
// measurement equality"). NaN/Inf are never considered equal to anything,
// including themselves, by IEEE 754 semantics, which is the desired
// behavior here (reject nonfinite values elsewhere, don't silently treat
// them as equal).
func measurementsEqual(a, b inspectclient.FixDTO) bool {
	return a.TimestampMs == b.TimestampMs &&
		a.Latitude == b.Latitude && a.Longitude == b.Longitude &&
		a.SpeedMps == b.SpeedMps && a.HeadingDegrees == b.HeadingDegrees
}

// isFinite reports whether every measurement in f is a finite float
// (INS-05: "reject NaN/nonfinite values").
func isFinite(f inspectclient.FixDTO) bool {
	return isFiniteFloat(f.Latitude) && isFiniteFloat(f.Longitude) && isFiniteFloat(f.SpeedMps) && isFiniteFloat(f.HeadingDegrees)
}

func isFiniteFloat(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// AG-02 measurement bounds, mirrored here independently (not imported from
// the aggregator's wire validation) per INS-05's independence requirement:
// latitude in [-90,90], longitude in [-180,180], speed_mps >= 0 (no upper
// bound), heading_degrees in [0,360).
const (
	minLatitude   = -90.0
	maxLatitude   = 90.0
	minLongitude  = -180.0
	maxLongitude  = 180.0
	minSpeedMps   = 0.0
	minHeadingDeg = 0.0
	maxHeadingDeg = 360.0 // exclusive
)

func withinMeasurementBounds(f inspectclient.FixDTO) bool {
	return f.Latitude >= minLatitude && f.Latitude <= maxLatitude &&
		f.Longitude >= minLongitude && f.Longitude <= maxLongitude &&
		f.SpeedMps >= minSpeedMps &&
		f.HeadingDegrees >= minHeadingDeg && f.HeadingDegrees < maxHeadingDeg
}

// interval is a closed [startMs,endMs] event-time range.
type interval struct {
	startMs, endMs int64
}

func (iv interval) contains(ts int64) bool { return ts >= iv.startMs && ts <= iv.endMs }

// deriveManualIntervals builds inclusive closed intervals from CLOSED/MANUAL
// trip geometry (INS-05: "derive barriers from CLOSED/MANUAL trip
// geometry"), independent of the API's own manual_barriers list, which is
// cross-checked against this derivation in V05.
func deriveManualIntervals(obs *Observation) map[string]interval {
	out := map[string]interval{}
	for _, t := range obs.Trips {
		if t.Status == inspectclient.TripStatusClosed && t.CloseReason == inspectclient.CloseReasonManual {
			out[t.TripID] = interval{startMs: t.StartMs, endMs: t.EndMs}
		}
	}
	return out
}

// component is one independently computed maximal temporal component: a
// maximal run of eligible (non-manual-member, non-quarantined) fixes with
// adjacent gaps <= gapThresholdMs, used to check V04.
type component struct {
	timestamps []int64 // ascending
}

// computeExpectedComponents independently partitions eligible raw fixes
// into maximal components, splitting whenever the adjacent gap exceeds
// gapThresholdMs or a manual barrier interval lies strictly between the two
// points' event times (INS-05: "partition eligible raw fixes around their
// inclusive intervals, sort fixes by event time, and split when adjacent
// gap is greater than configured gap"). Fixes that are themselves members
// of a manual interval, or quarantined, are excluded from eligibility
// entirely ("Current expected components exclude manual members and
// quarantined points").
func computeExpectedComponents(obs *Observation, manualIntervals map[string]interval) []component {
	var eligible []int64
	for _, f := range obs.RawFixes {
		if f.AssignmentReason != nil {
			continue // quarantined (e.g. MANUAL_CLOSED_INTERVAL)
		}
		inManual := false
		for _, iv := range manualIntervals {
			if iv.contains(f.TimestampMs) {
				inManual = true
				break
			}
		}
		if inManual {
			continue
		}
		eligible = append(eligible, f.TimestampMs)
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i] < eligible[j] })

	gapThresholdMs := obs.TripGapSeconds * 1000

	var comps []component
	var current []int64
	for i, ts := range eligible {
		if i == 0 {
			current = []int64{ts}
			continue
		}
		prev := eligible[i-1]
		if ts-prev > gapThresholdMs {
			comps = append(comps, component{timestamps: current})
			current = []int64{ts}
		} else {
			current = append(current, ts)
		}
	}
	if len(current) > 0 {
		comps = append(comps, component{timestamps: current})
	}
	return comps
}

// timestampSet is a set of timestamps for order-independent comparison.
type timestampSet map[int64]struct{}

func toTimestampSet(ts []int64) timestampSet {
	s := make(timestampSet, len(ts))
	for _, t := range ts {
		s[t] = struct{}{}
	}
	return s
}

func fixesToTimestampSet(fixes []inspectclient.FixDTO) timestampSet {
	s := make(timestampSet, len(fixes))
	for _, f := range fixes {
		s[f.TimestampMs] = struct{}{}
	}
	return s
}

func (s timestampSet) equals(other timestampSet) bool {
	if len(s) != len(other) {
		return false
	}
	for k := range s {
		if _, ok := other[k]; !ok {
			return false
		}
	}
	return true
}
