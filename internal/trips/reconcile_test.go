package trips

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

const gapMs = 600_000 // 600 seconds

func timestampsOf(c Component) []int64 { return c.Timestamps }

func TestReconcile_BasicSegmentation(t *testing.T) {
	// T+{0,600,1800,2400}s -> gaps of 600s(=600000ms, inclusive->connect),
	// 1200s(>600s->split), 600s(connect). So {0,600} join, {1800,2400} join:
	// two components.
	base := int64(1_700_000_000_000)
	ts := []int64{base + 0, base + 600_000, base + 1_800_000, base + 2_400_000}
	res, err := Reconcile(Params{
		GapMs:              gapMs,
		EligibleTimestamps: ts,
		MaxFixTimestamp:    ts[len(ts)-1],
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Components) != 2 {
		t.Fatalf("expected 2 components, got %d: %+v", len(res.Components), res.Components)
	}
	if !reflect.DeepEqual(res.Components[0].Timestamps, []int64{base, base + 600_000}) {
		t.Fatalf("unexpected first component: %+v", res.Components[0])
	}
	if !reflect.DeepEqual(res.Components[1].Timestamps, []int64{base + 1_800_000, base + 2_400_000}) {
		t.Fatalf("unexpected second component: %+v", res.Components[1])
	}
	if res.ActiveComponentIndex != 1 {
		t.Fatalf("expected active index 1, got %d", res.ActiveComponentIndex)
	}
}

func TestReconcile_MergeOnBridge(t *testing.T) {
	base := int64(1_700_000_000_000)
	// Existing trips from the basic segmentation above.
	existing := []ExistingTrip{
		{TripID: "trip-a", Status: StatusInactive, Members: []int64{base, base + 600_000}},
		{TripID: "trip-b", Status: StatusActive, Members: []int64{base + 1_800_000, base + 2_400_000}},
	}
	// Insert a bridging fix at +1200s, connecting both: gaps become 600s and
	// 600s, both exactly at the boundary (connect).
	all := []int64{base, base + 600_000, base + 1_200_000, base + 1_800_000, base + 2_400_000}
	res, err := Reconcile(Params{
		GapMs:              gapMs,
		Existing:           existing,
		EligibleTimestamps: all,
		MaxFixTimestamp:    base + 2_400_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Components) != 1 {
		t.Fatalf("expected 1 merged component, got %d: %+v", len(res.Components), res.Components)
	}
	comp := res.Components[0]
	if len(comp.Predecessors) != 2 || comp.Predecessors[0] != "trip-a" || comp.Predecessors[1] != "trip-b" {
		t.Fatalf("expected both predecessors, got %+v", comp.Predecessors)
	}
	if res.ActiveComponentIndex != 0 {
		t.Fatalf("expected active index 0, got %d", res.ActiveComponentIndex)
	}
}

func TestReconcile_SinglePredecessorContinues(t *testing.T) {
	base := int64(1_700_000_000_000)
	existing := []ExistingTrip{
		{TripID: "trip-a", Status: StatusActive, Members: []int64{base}},
	}
	all := []int64{base, base + 1000}
	res, err := Reconcile(Params{
		GapMs:              gapMs,
		Existing:           existing,
		EligibleTimestamps: all,
		MaxFixTimestamp:    base + 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Components) != 1 {
		t.Fatalf("expected 1 component, got %d", len(res.Components))
	}
	if len(res.Components[0].Predecessors) != 1 || res.Components[0].Predecessors[0] != "trip-a" {
		t.Fatalf("expected single predecessor trip-a, got %+v", res.Components[0].Predecessors)
	}
}

func TestReconcile_NoPredecessorIsNew(t *testing.T) {
	base := int64(1_700_000_000_000)
	res, err := Reconcile(Params{
		GapMs:              gapMs,
		EligibleTimestamps: []int64{base},
		MaxFixTimestamp:    base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Components[0].Predecessors) != 0 {
		t.Fatalf("expected no predecessors, got %+v", res.Components[0].Predecessors)
	}
}

func TestReconcile_BarrierPreventsConnection(t *testing.T) {
	base := int64(1_700_000_000_000)
	// Two timestamps 100ms apart (well within gap) but a barrier sits
	// between them, so they must never connect.
	ts := []int64{base, base + 100}
	res, err := Reconcile(Params{
		GapMs: gapMs,
		Barriers: []BarrierInterval{
			{StartMs: base + 50, EndMs: base + 60},
		},
		EligibleTimestamps: ts,
		MaxFixTimestamp:    base + 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Components) != 2 {
		t.Fatalf("expected 2 components across barrier, got %d: %+v", len(res.Components), res.Components)
	}
}

func TestReconcile_GapBoundaries(t *testing.T) {
	base := int64(1_700_000_000_000)
	cases := []struct {
		name       string
		gap        int64
		wantSingle bool
	}{
		{"599999_connects", 599_999, true},
		{"600000_connects_inclusive", 600_000, true},
		{"600001_splits", 600_001, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := []int64{base, base + c.gap}
			res, err := Reconcile(Params{
				GapMs:              gapMs,
				EligibleTimestamps: ts,
				MaxFixTimestamp:    base + c.gap,
			})
			if err != nil {
				t.Fatal(err)
			}
			gotSingle := len(res.Components) == 1
			if gotSingle != c.wantSingle {
				t.Fatalf("gap=%d: expected single=%v, got %d components", c.gap, c.wantSingle, len(res.Components))
			}
		})
	}
}

func TestReconcile_SplitInvariantViolation(t *testing.T) {
	base := int64(1_700_000_000_000)
	// Existing trip claims membership of two fixes that end up in different
	// components (gap too large) -- this must be reported as an error, not
	// silently resolved.
	existing := []ExistingTrip{
		{TripID: "trip-a", Status: StatusInactive, Members: []int64{base, base + 2*gapMs}},
	}
	all := []int64{base, base + 2*gapMs}
	_, err := Reconcile(Params{
		GapMs:              gapMs,
		Existing:           existing,
		EligibleTimestamps: all,
		MaxFixTimestamp:    base + 2*gapMs,
	})
	if err == nil {
		t.Fatal("expected invariant violation error")
	}
}

// TestReconcile_PermutationInvariance shuffles the same valid fix set across
// many seeded permutations, verifying identical final membership/boundaries
// regardless of arrival order within one reconstruction call (T08's
// single-shot analogue; full packet-by-packet permutation is an aggregator
// integration test).
func TestReconcile_PermutationInvariance(t *testing.T) {
	base := int64(1_700_000_000_000)
	offsets := []int64{0, 100_000, 600_000, 1_300_000, 1_900_000, 5_000_000}
	for seed := int64(0); seed < 50; seed++ {
		r := rand.New(rand.NewSource(seed))
		shuffled := append([]int64(nil), offsets...)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		ts := make([]int64, len(shuffled))
		for i, off := range shuffled {
			ts[i] = base + off
		}
		res, err := Reconcile(Params{
			GapMs:              gapMs,
			EligibleTimestamps: ts,
			MaxFixTimestamp:    base + 5_000_000,
		})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		var allTs []int64
		for _, c := range res.Components {
			allTs = append(allTs, c.Timestamps...)
		}
		sort.Slice(allTs, func(i, j int) bool { return allTs[i] < allTs[j] })
		wantTs := make([]int64, len(offsets))
		for i, off := range offsets {
			wantTs[i] = base + off
		}
		if !reflect.DeepEqual(allTs, wantTs) {
			t.Fatalf("seed %d: membership mismatch: got %v want %v", seed, allTs, wantTs)
		}
	}
}
