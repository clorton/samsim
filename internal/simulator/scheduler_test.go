package simulator

import "testing"

func TestFairnessSelector_DefaultPattern_BothEligible(t *testing.T) {
	f := NewFairnessSelector(3, 1)
	var got []Lane
	for i := 0; i < 8; i++ {
		lane, ok := f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
		if !ok {
			t.Fatalf("iteration %d: expected a lane", i)
		}
		got = append(got, lane)
		f.Advance()
	}
	want := []Lane{LaneLive, LaneLive, LaneLive, LaneBacklog, LaneLive, LaneLive, LaneLive, LaneBacklog}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: got %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestFairnessSelector_BacklogGuaranteedOnePerFour(t *testing.T) {
	f := NewFairnessSelector(3, 1)
	backlogCount := 0
	for i := 0; i < 4; i++ {
		lane, ok := f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
		if !ok {
			t.Fatalf("iteration %d: expected a lane", i)
		}
		if lane == LaneBacklog {
			backlogCount++
		}
		f.Advance()
	}
	if backlogCount != 1 {
		t.Fatalf("expected exactly 1 backlog slot per 4 ordinary attempts, got %d", backlogCount)
	}
}

func TestFairnessSelector_EmptyLaneUsesSpareSlotsForOther(t *testing.T) {
	// Backlog never eligible: every slot (including the pattern's backlog
	// slots) must go to live.
	f := NewFairnessSelector(3, 1)
	for i := 0; i < 8; i++ {
		lane, ok := f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: false})
		if !ok || lane != LaneLive {
			t.Fatalf("iteration %d: expected live (backlog ineligible), got %s ok=%v", i, lane, ok)
		}
		f.Advance()
	}
}

func TestFairnessSelector_LiveIneligibleUsesBacklog(t *testing.T) {
	f := NewFairnessSelector(3, 1)
	for i := 0; i < 8; i++ {
		lane, ok := f.Select(LaneEligibility{LiveEligible: false, BacklogEligible: true})
		if !ok || lane != LaneBacklog {
			t.Fatalf("iteration %d: expected backlog (live ineligible), got %s ok=%v", i, lane, ok)
		}
		f.Advance()
	}
}

func TestFairnessSelector_NeitherEligible(t *testing.T) {
	f := NewFairnessSelector(3, 1)
	_, ok := f.Select(LaneEligibility{LiveEligible: false, BacklogEligible: false})
	if ok {
		t.Fatal("expected no lane selected when neither eligible")
	}
}

func TestFairnessSelector_SelectWithoutAdvanceIsIdempotent(t *testing.T) {
	f := NewFairnessSelector(3, 1)
	lane1, _ := f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
	lane2, _ := f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
	if lane1 != lane2 {
		t.Fatalf("expected repeated Select without Advance to be idempotent: %s vs %s", lane1, lane2)
	}
}

func TestFairnessSelector_SetWeightsResetsCycle(t *testing.T) {
	f := NewFairnessSelector(3, 1)
	f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
	f.Advance()
	f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
	f.Advance()
	f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
	f.Advance() // cyclePos now 3 (next would be backlog)

	f.SetWeights(1, 1) // reset cycle; new pattern is L,B

	lane, _ := f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
	if lane != LaneLive {
		t.Fatalf("expected cycle reset to start at live, got %s", lane)
	}
}

func TestFairnessSelector_DefensiveMinimumWeight(t *testing.T) {
	f := NewFairnessSelector(0, -1)
	// Should not panic (modulo by zero); both clamp to 1.
	lane, ok := f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
	if !ok || lane != LaneLive {
		t.Fatalf("expected live first with clamped weights, got %s ok=%v", lane, ok)
	}
}

func TestFairnessSelector_CustomWeights(t *testing.T) {
	f := NewFairnessSelector(2, 2)
	var got []Lane
	for i := 0; i < 4; i++ {
		lane, _ := f.Select(LaneEligibility{LiveEligible: true, BacklogEligible: true})
		got = append(got, lane)
		f.Advance()
	}
	want := []Lane{LaneLive, LaneLive, LaneBacklog, LaneBacklog}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: got %s, want %s", i, got[i], want[i])
		}
	}
}
