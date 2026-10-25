package simulator

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestFakeMonotonicClock_StartsAtGivenInstant(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewFakeMonotonicClock(start)
	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("expected %v, got %v", start, got)
	}
}

func TestFakeMonotonicClock_Advance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewFakeMonotonicClock(start)
	c.Advance(5 * time.Second)
	want := start.Add(5 * time.Second)
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	c.Advance(250 * time.Millisecond)
	want = want.Add(250 * time.Millisecond)
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestFakeMonotonicClock_ConcurrentSafe(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewFakeMonotonicClock(start)

	var wg sync.WaitGroup
	const n = 100
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			c.Advance(time.Millisecond)
		}()
		go func() {
			defer wg.Done()
			_ = c.Now()
		}()
	}
	wg.Wait()

	got := c.Now()
	want := start.Add(n * time.Millisecond)
	if !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestVirtualClock_FreshIsZero(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 100)
	if got := vc.VirtualElapsedMs(); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
	if vc.Running() {
		t.Fatal("expected fresh clock to not be running")
	}
}

func TestVirtualClock_ExactMultiplierAdvancement(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 100)
	vc.Start()
	mono.Advance(1500 * time.Millisecond) // 1.5s
	// 1.5s * 100 = 150s = 150000ms
	if got := vc.VirtualElapsedMs(); got != 150000 {
		t.Fatalf("expected exactly 150000ms, got %d", got)
	}
}

func TestVirtualClock_StopFreezesValue(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 100)
	vc.Start()
	mono.Advance(1 * time.Second)
	if got := vc.VirtualElapsedMs(); got != 100000 {
		t.Fatalf("expected 100000ms before stop, got %d", got)
	}
	vc.Stop()
	frozen := vc.VirtualElapsedMs()
	if frozen != 100000 {
		t.Fatalf("expected frozen value 100000ms, got %d", frozen)
	}
	mono.Advance(10 * time.Second)
	if got := vc.VirtualElapsedMs(); got != frozen {
		t.Fatalf("expected value to remain frozen at %d after further advance, got %d", frozen, got)
	}
}

func TestVirtualClock_ResumeFromFrozenValue(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 10)

	vc.Start()
	mono.Advance(1 * time.Second) // 1s * 10 = 10000ms
	if got := vc.VirtualElapsedMs(); got != 10000 {
		t.Fatalf("expected 10000ms, got %d", got)
	}

	vc.Stop()
	mono.Advance(5 * time.Second) // should have zero effect while stopped
	if got := vc.VirtualElapsedMs(); got != 10000 {
		t.Fatalf("expected 10000ms while stopped (unaffected by wall time), got %d", got)
	}

	vc.Start()
	mono.Advance(1 * time.Second) // another 1s * 10 = 10000ms
	if got := vc.VirtualElapsedMs(); got != 20000 {
		t.Fatalf("expected 20000ms after resume, got %d", got)
	}
}

func TestVirtualClock_SetMultiplierWhileRunningIsContinuous(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 10)
	vc.Start()
	mono.Advance(1 * time.Second) // 10 * 1s = 10000ms
	if got := vc.VirtualElapsedMs(); got != 10000 {
		t.Fatalf("expected 10000ms, got %d", got)
	}

	before := vc.VirtualElapsedMs()
	vc.SetMultiplier(100)
	after := vc.VirtualElapsedMs()
	if before != after {
		t.Fatalf("expected no jump at multiplier change instant: before=%d after=%d", before, after)
	}

	mono.Advance(1 * time.Second) // 100 * 1s = 100000ms
	want := int64(10000 + 100000)
	if got := vc.VirtualElapsedMs(); got != want {
		t.Fatalf("expected %d after multiplier change and further advance, got %d", want, got)
	}
}

func TestVirtualClock_SetMultiplierWhileStopped(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 10)
	vc.Start()
	mono.Advance(1 * time.Second) // 10000ms
	vc.Stop()
	frozen := vc.VirtualElapsedMs()
	if frozen != 10000 {
		t.Fatalf("expected 10000ms frozen, got %d", frozen)
	}

	vc.SetMultiplier(999)
	if got := vc.VirtualElapsedMs(); got != frozen {
		t.Fatalf("expected frozen value unchanged by SetMultiplier while stopped, got %d", got)
	}
	if got := vc.Multiplier(); got != 999 {
		t.Fatalf("expected multiplier to be updated to 999, got %v", got)
	}

	// Confirm the new multiplier takes effect on next Start.
	vc.Start()
	mono.Advance(1 * time.Second) // 999 * 1s = 999000ms
	want := int64(10000 + 999000)
	if got := vc.VirtualElapsedMs(); got != want {
		t.Fatalf("expected %d after resuming with new multiplier, got %d", want, got)
	}
}

func TestVirtualClock_StartWhileRunningIsNoOp(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 10)
	vc.Start()
	mono.Advance(1 * time.Second) // 10000ms

	vc.Start() // redundant, should be a no-op

	mono.Advance(1 * time.Second) // another 10000ms
	want := int64(20000)
	if got := vc.VirtualElapsedMs(); got != want {
		t.Fatalf("expected %d (redundant Start should not reset anchor), got %d", want, got)
	}
}

func TestVirtualClock_StopWhileStoppedIsNoOp(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 10)

	// Stopped from the start; calling Stop should not panic or change anything.
	vc.Stop()
	if got := vc.VirtualElapsedMs(); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}

	vc.Start()
	mono.Advance(1 * time.Second) // 10000ms
	vc.Stop()
	frozen := vc.VirtualElapsedMs()
	if frozen != 10000 {
		t.Fatalf("expected 10000ms, got %d", frozen)
	}

	// Multiple redundant stops.
	vc.Stop()
	vc.Stop()
	mono.Advance(5 * time.Second)
	if got := vc.VirtualElapsedMs(); got != frozen {
		t.Fatalf("expected frozen value %d unchanged after redundant stops, got %d", frozen, got)
	}
}

func TestVirtualClock_NoDriftOverManyQueries(t *testing.T) {
	start := time.Now()
	mono := NewFakeMonotonicClock(start)
	const multiplier = 3.0
	vc := NewVirtualClock(mono, multiplier)
	vc.Start()

	const iterations = 10000
	const stepNanos = 1234567 // irregular, doesn't divide evenly into a ms

	var lastReading int64
	for i := 0; i < iterations; i++ {
		mono.Advance(stepNanos * time.Nanosecond)
		lastReading = vc.VirtualElapsedMs()
	}

	totalElapsed := mono.Now().Sub(start)
	expected := int64(math.Round(totalElapsed.Seconds() * 1000 * multiplier))

	diff := lastReading - expected
	if diff < 0 {
		diff = -diff
	}
	if diff > 1 {
		t.Fatalf("drift detected: expected ~%d ms, got %d ms (diff %d ms)", expected, lastReading, diff)
	}
}

func TestVirtualClock_MultiplierGetter(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 42)
	if got := vc.Multiplier(); got != 42 {
		t.Fatalf("expected 42, got %v", got)
	}
	vc.SetMultiplier(7)
	if got := vc.Multiplier(); got != 7 {
		t.Fatalf("expected 7, got %v", got)
	}
}

func TestVirtualClock_RunningGetter(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 1)
	if vc.Running() {
		t.Fatal("expected fresh clock to not be running")
	}
	vc.Start()
	if !vc.Running() {
		t.Fatal("expected clock to be running after Start")
	}
	vc.Stop()
	if vc.Running() {
		t.Fatal("expected clock to not be running after Stop")
	}
}

func TestChunkDueIndices(t *testing.T) {
	cases := []struct {
		name         string
		start, end   int
		maxChunkSize int
		want         [][2]int
	}{
		{"example from spec", 0, 2500, 1000, [][2]int{{0, 1000}, {1000, 2000}, {2000, 2500}}},
		{"exact multiple", 0, 3000, 1000, [][2]int{{0, 1000}, {1000, 2000}, {2000, 3000}}},
		{"smaller than one chunk", 0, 500, 1000, [][2]int{{0, 500}}},
		{"empty range", 5, 5, 1000, nil},
		{"chunk size of 1", 0, 3, 1, [][2]int{{0, 1}, {1, 2}, {2, 3}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChunkDueIndices(tc.start, tc.end, tc.maxChunkSize)
			if len(got) != len(tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("expected %v, got %v", tc.want, got)
				}
			}
		})
	}
}

func TestChunkDueIndices_InvalidInputs(t *testing.T) {
	if got := ChunkDueIndices(10, 5, 1000); got != nil {
		t.Fatalf("expected nil for end < start, got %v", got)
	}
	if got := ChunkDueIndices(0, 100, 0); got != nil {
		t.Fatalf("expected nil for non-positive maxChunkSize, got %v", got)
	}
	if got := ChunkDueIndices(0, 100, -5); got != nil {
		t.Fatalf("expected nil for negative maxChunkSize, got %v", got)
	}
}

func TestVirtualClock_ConcurrencySmoke(t *testing.T) {
	mono := NewFakeMonotonicClock(time.Now())
	vc := NewVirtualClock(mono, 10)
	vc.Start()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = vc.VirtualElapsedMs()
					_ = vc.Running()
					_ = vc.Multiplier()
				}
			}
		}()
	}

	// Writer: toggles state a few times, then signals readers to stop.
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for i := 0; i < 50; i++ {
			mono.Advance(time.Millisecond)
			if i%5 == 0 {
				vc.Stop()
			}
			if i%5 == 2 {
				vc.Start()
			}
			if i%7 == 0 {
				vc.SetMultiplier(float64(i + 1))
			}
		}
	}()

	writerWG.Wait()
	close(stop)
	wg.Wait()
}
