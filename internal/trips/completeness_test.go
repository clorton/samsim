package trips

import "testing"

func TestAnalyzeCompleteness_RegularCadence(t *testing.T) {
	ts := []int64{1000, 2000, 3000, 4000}
	c := AnalyzeCompleteness(ts, 1000)
	if !c.Complete {
		t.Fatalf("expected complete, got %+v", c)
	}
	if c.MissingFixCount != 0 {
		t.Fatalf("expected 0 missing, got %d", c.MissingFixCount)
	}
}

func TestAnalyzeCompleteness_LargeGap(t *testing.T) {
	// gap 11000ms, interval 1000ms -> 10 missing.
	ts := []int64{1000, 12000}
	c := AnalyzeCompleteness(ts, 1000)
	if c.Complete {
		t.Fatalf("expected incomplete")
	}
	if c.MissingFixCount != 10 {
		t.Fatalf("expected 10 missing, got %d", c.MissingFixCount)
	}
	if len(c.MissingIntervals) != 1 {
		t.Fatalf("expected 1 interval, got %d", len(c.MissingIntervals))
	}
	iv := c.MissingIntervals[0]
	if iv.StartMs != 2000 || iv.EndMs != 11000 || iv.MissingCount != 10 {
		t.Fatalf("unexpected interval: %+v", iv)
	}
}

func TestAnalyzeCompleteness_SmallGap(t *testing.T) {
	// gap 1500ms, interval 1000ms -> 1 expected slot.
	ts := []int64{1000, 2500}
	c := AnalyzeCompleteness(ts, 1000)
	if c.MissingFixCount != 1 {
		t.Fatalf("expected 1 missing, got %d", c.MissingFixCount)
	}
	iv := c.MissingIntervals[0]
	if iv.StartMs != 2000 || iv.EndMs != 2000 || iv.MissingCount != 1 {
		t.Fatalf("unexpected interval: %+v", iv)
	}
}

func TestAnalyzeCompleteness_Singleton(t *testing.T) {
	c := AnalyzeCompleteness([]int64{1000}, 1000)
	if !c.Complete || c.MissingFixCount != 0 {
		t.Fatalf("expected complete singleton, got %+v", c)
	}
}
