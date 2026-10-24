// Package trips implements pure event-time trip segmentation and merge
// reconciliation (AG-05), decoupled from storage so it can be tested with
// plain in-memory fixtures (T05-T09). It operates purely on fix identity
// (timestamps) and trip membership; callers are responsible for persistence,
// ID allocation and coordinate bookkeeping.
package trips

import (
	"fmt"
	"sort"
)

// Status values for mutable (non-closed) trips considered by reconciliation.
const (
	StatusActive   = "ACTIVE"
	StatusInactive = "INACTIVE"
)

// BarrierInterval is an immutable manually-closed interval [StartMs, EndMs]
// that permanently partitions event time; no mutable component may cross it.
type BarrierInterval struct {
	StartMs int64
	EndMs   int64
}

// ExistingTrip is a pretransaction mutable (ACTIVE or INACTIVE) trip and its
// current fix membership, used to detect shared-membership predecessors.
type ExistingTrip struct {
	TripID  string
	Status  string
	Members []int64 // fix timestamps currently assigned to this trip
}

// Component is one maximal connected set of eligible fix timestamps within
// one barrier partition, after splitting on gaps larger than GapMs.
type Component struct {
	// Timestamps is sorted ascending and contains unique fix timestamps.
	Timestamps []int64
	// Predecessors holds the sorted IDs of pretransaction mutable trips that
	// share at least one member with this component. Zero predecessors means
	// a brand-new trip; exactly one means the trip continues under its
	// existing ID; two or more means those trips merge into a new ID.
	Predecessors []string
}

// Params is the input to Reconcile.
type Params struct {
	// GapMs is the configured trip_gap_seconds, in milliseconds. Consecutive
	// eligible timestamps whose difference exceeds GapMs start a new
	// component.
	GapMs int64
	// Barriers lists all immutable manual-closure intervals for the vehicle,
	// sorted ascending by StartMs and guaranteed nonoverlapping by the
	// caller.
	Barriers []BarrierInterval
	// Existing lists all pretransaction ACTIVE/INACTIVE trips for the
	// vehicle.
	Existing []ExistingTrip
	// EligibleTimestamps is the complete set of non-quarantined fix
	// timestamps that participate in mutable reconstruction: the union of
	// every ExistingTrip's Members and all newly accepted fixes outside any
	// barrier. Must not contain duplicates; order does not matter.
	EligibleTimestamps []int64
	// MaxFixTimestamp is the maximum raw event timestamp over all valid
	// stored fixes for the vehicle (including quarantined and
	// closed-assigned fixes). Used to determine the active pointer. Use a
	// negative value if the vehicle has no fixes at all.
	MaxFixTimestamp int64
}

// Result is the output of Reconcile.
type Result struct {
	// Components holds every resulting mutable component, ordered by its
	// minimum timestamp ascending.
	Components []Component
	// ActiveComponentIndex is the index into Components whose trip should be
	// ACTIVE, or -1 if no mutable component should be active (the latest fix
	// is quarantined, inside a closed interval, or there are no eligible
	// fixes at all).
	ActiveComponentIndex int
}

// Reconcile partitions EligibleTimestamps around Barriers, splits each
// partition into gap-delimited components, and matches each component
// against Existing trips by shared membership (AG-05 steps 1-4).
//
// It returns an error if an existing trip's membership is not fully
// contained within exactly one resulting component: under additions-only
// growth, mutable components can only grow or merge, never split, so this
// indicates an invariant violation in the caller's bookkeeping.
func Reconcile(p Params) (Result, error) {
	timestamps := dedupeSorted(p.EligibleTimestamps)

	partitions := partitionByBarriers(timestamps, p.Barriers)

	var components []Component
	for _, part := range partitions {
		components = append(components, splitByGap(part, p.GapMs)...)
	}

	memberToComponent := make(map[int64]int, len(timestamps))
	for ci, c := range components {
		for _, ts := range c.Timestamps {
			memberToComponent[ts] = ci
		}
	}

	predSets := make([]map[string]struct{}, len(components))
	for i := range predSets {
		predSets[i] = map[string]struct{}{}
	}

	for _, et := range p.Existing {
		seen := map[int]bool{}
		for _, m := range et.Members {
			ci, ok := memberToComponent[m]
			if !ok {
				return Result{}, fmt.Errorf("trips: existing trip %q member %d is not in any eligible component (dropped fix?)", et.TripID, m)
			}
			seen[ci] = true
		}
		if len(seen) > 1 {
			return Result{}, fmt.Errorf("trips: invariant violation: existing trip %q split across %d components", et.TripID, len(seen))
		}
		for ci := range seen {
			predSets[ci][et.TripID] = struct{}{}
		}
	}

	for i := range components {
		var preds []string
		for id := range predSets[i] {
			preds = append(preds, id)
		}
		sort.Strings(preds)
		components[i].Predecessors = preds
	}

	activeIdx := -1
	if p.MaxFixTimestamp >= 0 {
		if ci, ok := memberToComponent[p.MaxFixTimestamp]; ok {
			activeIdx = ci
		}
	}

	return Result{Components: components, ActiveComponentIndex: activeIdx}, nil
}

func dedupeSorted(ts []int64) []int64 {
	if len(ts) == 0 {
		return nil
	}
	out := make([]int64, len(ts))
	copy(out, ts)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	for i, v := range out {
		if i == 0 || v != out[i-1] {
			out[n] = v
			n++
		}
	}
	return out[:n]
}

// partitionByBarriers splits sorted unique timestamps into the open
// intervals before, between and after the sorted nonoverlapping barriers.
// Empty partitions are omitted.
func partitionByBarriers(sorted []int64, barriers []BarrierInterval) [][]int64 {
	if len(barriers) == 0 {
		if len(sorted) == 0 {
			return nil
		}
		return [][]int64{sorted}
	}
	var partitions [][]int64
	bi := 0
	var current []int64
	for _, ts := range sorted {
		for bi < len(barriers) && ts > barriers[bi].EndMs {
			if len(current) > 0 {
				partitions = append(partitions, current)
				current = nil
			}
			bi++
		}
		// ts must not fall within [start,end] of barriers[bi] by caller
		// contract (that fix would have been quarantined instead).
		current = append(current, ts)
	}
	if len(current) > 0 {
		partitions = append(partitions, current)
	}
	return partitions
}

// splitByGap splits a sorted unique timestamp partition into components
// wherever the gap between consecutive timestamps exceeds gapMs.
func splitByGap(sorted []int64, gapMs int64) []Component {
	if len(sorted) == 0 {
		return nil
	}
	var components []Component
	start := 0
	for i := 1; i < len(sorted); i++ {
		if sorted[i]-sorted[i-1] > gapMs {
			components = append(components, Component{Timestamps: append([]int64(nil), sorted[start:i]...)})
			start = i
		}
	}
	components = append(components, Component{Timestamps: append([]int64(nil), sorted[start:]...)})
	return components
}
