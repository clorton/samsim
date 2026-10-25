package simulator

// Lane identifies which FIFO queue a record or packet belongs to (SIM-04).
type Lane string

const (
	LaneLive    Lane = "live"
	LaneBacklog Lane = "backlog"
)

// LaneEligibility describes whether each lane currently qualifies for an
// ordinary (non-recovery-gated) send attempt, per SIM-04: "A lane is
// eligible when it has packet_size records, its oldest queued record has
// waited partial_flush_wall_ms in wall time, or EOF/recovery requires
// flushing."
type LaneEligibility struct {
	LiveEligible    bool
	BacklogEligible bool
}

// FairnessSelector implements SIM-04's weighted live/backlog packet
// selection: a repeating pattern of LiveWeight live slots followed by
// BacklogWeight backlog slots (default 3 live, 1 backlog, i.e. L,L,L,B),
// advanced only on actual ordinary send attempts (never on empty timer
// ticks), with spare slots given to whichever lane is eligible when the
// pattern's preferred lane is not.
//
// This type holds no I/O or queue data; it is a pure decision function over
// a cycle position, making it easy to test in isolation from the rest of
// the engine.
type FairnessSelector struct {
	liveWeight    int
	backlogWeight int
	cyclePos      int // position within the current pattern, advanced only on actual sends
}

// NewFairnessSelector returns a selector for the given weights. Weights
// must be >= 1 (validated by Config.Validate elsewhere); a nonpositive
// weight is treated as 1 defensively.
func NewFairnessSelector(liveWeight, backlogWeight int) *FairnessSelector {
	if liveWeight < 1 {
		liveWeight = 1
	}
	if backlogWeight < 1 {
		backlogWeight = 1
	}
	return &FairnessSelector{liveWeight: liveWeight, backlogWeight: backlogWeight}
}

// SetWeights changes the live/backlog weights. Per SIM-04, "Runtime weight
// changes reset the selection cycle at the next packet boundary" -- since
// this type only advances its cycle on an actual send (see Advance), simply
// resetting cyclePos to 0 here, to take effect starting from the next
// selection, satisfies that requirement.
func (f *FairnessSelector) SetWeights(liveWeight, backlogWeight int) {
	if liveWeight < 1 {
		liveWeight = 1
	}
	if backlogWeight < 1 {
		backlogWeight = 1
	}
	f.liveWeight = liveWeight
	f.backlogWeight = backlogWeight
	f.cyclePos = 0
}

// patternLen is the total length of one fairness cycle.
func (f *FairnessSelector) patternLen() int {
	return f.liveWeight + f.backlogWeight
}

// preferredLaneAt reports which lane the pattern prefers at position pos
// (0-indexed, wrapped to the pattern length): the first liveWeight slots
// are LaneLive, the remaining backlogWeight slots are LaneBacklog.
func (f *FairnessSelector) preferredLaneAt(pos int) Lane {
	pos %= f.patternLen()
	if pos < f.liveWeight {
		return LaneLive
	}
	return LaneBacklog
}

// Select decides which lane (if any) an ordinary send attempt should use,
// given current eligibility. It does NOT advance the cycle; call Advance
// separately once an attempt is actually dispatched (so that lane
// eligibility re-checks and retries of a single decision don't skip cycle
// positions). Returns ("", false) if neither lane is eligible.
func (f *FairnessSelector) Select(elig LaneEligibility) (Lane, bool) {
	if !elig.LiveEligible && !elig.BacklogEligible {
		return "", false
	}
	preferred := f.preferredLaneAt(f.cyclePos)
	switch preferred {
	case LaneLive:
		if elig.LiveEligible {
			return LaneLive, true
		}
		if elig.BacklogEligible {
			return LaneBacklog, true
		}
	case LaneBacklog:
		if elig.BacklogEligible {
			return LaneBacklog, true
		}
		if elig.LiveEligible {
			return LaneLive, true
		}
	}
	return "", false
}

// Advance moves the cycle forward by one position. Call this only after an
// actual ordinary send attempt is dispatched (not for empty ticks, and not
// for a retry of an already-pending/failed packet, which is not a new
// fairness selection per SIM-04).
func (f *FairnessSelector) Advance() {
	f.cyclePos = (f.cyclePos + 1) % f.patternLen()
}
