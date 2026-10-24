package trips

// MissingInterval describes a run of expected-but-absent fix slots between
// two observed timestamps, anchored at the earlier one (AG-06).
type MissingInterval struct {
	StartMs      int64
	EndMs        int64
	MissingCount int64
}

// Completeness is the result of an internal-gap cadence analysis over one
// trip's ordered snapshot timestamps. It is a cadence estimate anchored at
// each observed fix, not a guarantee of a global sampling grid, and makes no
// claim about boundary completeness.
type Completeness struct {
	MissingFixCount  int64
	MissingIntervals []MissingInterval
	Complete         bool
}

// AnalyzeCompleteness computes internal expected-cadence gaps over sorted
// unique ascending timestamps, given the expected fix interval in
// milliseconds (AG-06). For consecutive a,b the missing count is
// max(0, ceil((b-a)/interval)-1); when positive, the reported interval spans
// the expected slots a+interval through a+count*interval inclusive.
func AnalyzeCompleteness(sortedTimestamps []int64, expectedIntervalMs int64) Completeness {
	var result Completeness
	result.Complete = true
	if expectedIntervalMs <= 0 || len(sortedTimestamps) < 2 {
		return result
	}
	for i := 1; i < len(sortedTimestamps); i++ {
		a, b := sortedTimestamps[i-1], sortedTimestamps[i]
		gap := b - a
		if gap <= 0 {
			continue
		}
		// ceil(gap/interval) - 1, overflow-safe for the problem's bounded
		// timestamp range (signed 64-bit epoch milliseconds through year
		// 9999, interval >= 1).
		quotient := gap / expectedIntervalMs
		remainder := gap % expectedIntervalMs
		ceilDiv := quotient
		if remainder != 0 {
			ceilDiv++
		}
		missing := ceilDiv - 1
		if missing <= 0 {
			continue
		}
		result.Complete = false
		result.MissingFixCount += missing
		result.MissingIntervals = append(result.MissingIntervals, MissingInterval{
			StartMs:      a + expectedIntervalMs,
			EndMs:        a + missing*expectedIntervalMs,
			MissingCount: missing,
		})
	}
	return result
}
