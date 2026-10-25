package simulator

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// buildRouteCSV generates a simple route CSV with n rows at 1-second
// cadence starting at ts=0, suitable for most engine tests.
func buildRouteCSV(n int) string {
	var b strings.Builder
	b.WriteString("ts,lat,lon,speed_mps,heading_deg\n")
	for i := 0; i < n; i++ {
		lon := -120.0 + float64(i%100)*0.001
		fmt.Fprintf(&b, "%d.0,40.0,%v,1.0,0.0\n", i, lon)
	}
	return b.String()
}

func TestEngine_StartFromReady(t *testing.T) {
	e, _ := newTestEngine(t, nil, buildRouteCSV(5))
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	st := e.Status()
	if st.PlaybackState != PlaybackRunning {
		t.Fatalf("expected RUNNING, got %s", st.PlaybackState)
	}
	if st.RunID == nil || *st.RunID == "" {
		t.Fatal("expected a run_id")
	}
	if st.ResolvedRunStartMs == nil || *st.ResolvedRunStartMs != 1_700_000_000_000 {
		t.Fatalf("unexpected resolved_run_start_ms: %+v", st.ResolvedRunStartMs)
	}
}

func TestEngine_StartIdempotentWhileRunning(t *testing.T) {
	e, _ := newTestEngine(t, nil, buildRouteCSV(5))
	e.Start()
	runID1 := *e.Status().RunID
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if *e.Status().RunID != runID1 {
		t.Fatal("expected idempotent Start to preserve run_id")
	}
}

func TestEngine_StartAfterCompletedRequiresReset(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.PartialFlushWallMs = 1 }, buildRouteCSV(1))
	e.Start()
	deps.mono.Advance(1 * time.Second) // let the single row become due -> DRAINING
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1)
	e.Tick() // completion check
	if e.Status().PlaybackState != PlaybackCompleted {
		t.Fatalf("expected COMPLETED, got %s", e.Status().PlaybackState)
	}
	if err := e.Start(); err != ErrResetRequired {
		t.Fatalf("expected ErrResetRequired, got %v", err)
	}
}

func TestEngine_StopFreezesVirtualClock(t *testing.T) {
	e, deps := newTestEngine(t, nil, buildRouteCSV(100))
	e.Start()
	deps.mono.Advance(1 * time.Second)
	e.Stop()
	elapsedAtStop := e.Status().VirtualElapsedMs
	deps.mono.Advance(5 * time.Second)
	if e.Status().VirtualElapsedMs != elapsedAtStop {
		t.Fatalf("expected virtual elapsed frozen at %d, got %d", elapsedAtStop, e.Status().VirtualElapsedMs)
	}
}

func TestEngine_StopThenStartResumesWithoutCountingStoppedTime(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.ClockMultiplier = 10 }, buildRouteCSV(100))
	e.Start()
	deps.mono.Advance(1 * time.Second) // +10000ms virtual
	e.Stop()
	deps.mono.Advance(5 * time.Second) // must not count
	e.Start()
	deps.mono.Advance(1 * time.Second) // +10000ms virtual
	if got := e.Status().VirtualElapsedMs; got != 20000 {
		t.Fatalf("expected 20000ms virtual elapsed, got %d", got)
	}
}

func TestEngine_ResetRequiresStopped(t *testing.T) {
	e, _ := newTestEngine(t, nil, buildRouteCSV(5))
	e.Start()
	if _, err := e.Reset(false); err != ErrStopRequired {
		t.Fatalf("expected ErrStopRequired while RUNNING, got %v", err)
	}
}

func TestEngine_ResetPendingDataRequiresDiscard(t *testing.T) {
	// Generate a few rows (route has more remaining, so playback stays
	// RUNNING, not DRAINING) but never let them become eligible for sending
	// (packet_size not reached, partial_flush age not elapsed), so they
	// remain genuinely unsent in the live queue.
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 10
		c.PacketSize = 100
		c.PartialFlushWallMs = 999_000
	}, buildRouteCSV(1000))
	e.Start()
	deps.mono.Advance(500 * time.Millisecond) // multiplier 10 -> 5 virtual seconds -> 5 rows due; 995 remain
	e.Tick()
	if deps.sender.sentCount() != 0 {
		t.Fatalf("expected no sends yet (ineligible lane), got %d", deps.sender.sentCount())
	}
	e.Stop()

	if _, err := e.Reset(false); err != ErrPendingData {
		t.Fatalf("expected ErrPendingData, got %v", err)
	}
	result, err := e.Reset(true)
	if err != nil {
		t.Fatalf("expected discard to succeed, got %v", err)
	}
	if result.DiscardedFixes != 6 {
		t.Fatalf("expected 6 discarded fixes, got %d", result.DiscardedFixes)
	}
	if e.Status().PlaybackState != PlaybackReady {
		t.Fatalf("expected READY after reset, got %s", e.Status().PlaybackState)
	}
}

func TestEngine_ResetClearsCountersButKeepsConfig(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.PartialFlushWallMs = 1 }, buildRouteCSV(5))
	e.Start()
	deps.mono.Advance(10 * time.Second) // let all 5 rows become due -> DRAINING
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1)
	e.Stop()
	e.Reset(true)

	st := e.Status()
	if st.GeneratedFixes != 0 || st.AcknowledgedFixes != 0 {
		t.Fatalf("expected counters reset to 0, got %+v", st)
	}
	if st.Config.PacketSize != e.cfg.PacketSize {
		t.Fatal("expected config retained across reset")
	}
}

// waitForSendAndSettle ticks the engine until the fake sender has recorded
// n sends and the engine's pending state has settled (no longer in
// flight), using only the fake clock / synchronous Tick calls (never real
// sleeps beyond tiny polling waits for the async dispatch goroutine).
func waitForSendAndSettle(t testingT, e *Engine, deps *testEngineDeps, n int) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if deps.sender.sentCount() >= n {
			waitForPendingCleared(t, e)
			return
		}
		e.Tick()
		// A brief yield lets the async dispatch goroutine (launched by Tick)
		// actually run; without it this busy loop can spin through all its
		// iterations faster than that goroutine gets scheduled, especially
		// under -race. time.Sleep(0) / runtime.Gosched would not reliably
		// yield long enough under -race's instrumentation overhead, so a
		// short real sleep is used instead.
		time.Sleep(100 * time.Microsecond)
	}
	t.Fatalf("timed out waiting for %d sends; got %d", n, deps.sender.sentCount())
}
