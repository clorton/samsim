package simulator

import (
	"testing"
	"time"
)

// TestOverload_QueueGrowthAndCongestionNoDrop implements the spec's
// overload test: at multiplier=1000 with a 10-simulated-second report
// interval and a 1 Hz source, each closed window holds ~10 fixes, closing
// at an actual rate of 100 reports/s (10000ms/1000 -> 10ms actual interval
// -> 100/s demand). Configuring capacity below that demand (50/s actual)
// means queue growth and congested=true are expected, but no fixes may
// ever be dropped (the conservation invariant must still hold).
func TestOverload_QueueGrowthAndCongestionNoDrop(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 1000
		c.PacketSize = 10
		c.NormalReportIntervalSimulatedMs = 10000 // 10 actual ms per window at this multiplier -> 100 reports/s demand
		c.MaxPacketsPerSimulatedSecond = 0.05     // *1000 -> 50/s actual: insufficient for 100/s demand
	}, buildRouteCSV(10_000))

	e.Start()

	// Advance the fake monotonic clock in small increments, ticking
	// throughout, so both generation and (rate-limited) sending proceed
	// concurrently, as they would in the real background loop. The
	// congestion detector's sustained-growth fallback needs several
	// seconds of monotonic history before it's confident growth is
	// persistent (not a brief transient), so this advances a full 6
	// seconds of mono time.
	for i := 0; i < 30; i++ {
		deps.mono.Advance(200 * time.Millisecond) // *1000 -> 200 virtual seconds of generation per iteration
		e.Tick()
	}
	// One final settle wait for any in-flight async send to finish before
	// reading final status.
	waitForPendingCleared(t, e)

	st := e.Status()
	if st.BacklogQueueCount != 0 {
		t.Fatalf("expected no coverage outage in this test, backlog should stay empty, got %d", st.BacklogQueueCount)
	}
	if st.LiveQueueCount == 0 {
		t.Fatal("expected queue growth (live backlog) since demand exceeds configured capacity")
	}
	if !st.Congested {
		t.Fatalf("expected congested=true given sustained demand far exceeding capacity, got status=%+v", st)
	}
	if st.CongestionReason == nil || *st.CongestionReason == "" {
		t.Fatal("expected a congestion reason to be reported")
	}

	assertQueueInvariant(t, e)
	if st.GeneratedFixes == 0 {
		t.Fatal("expected substantial generation to have occurred")
	}

	t.Logf("overload test: generated=%d acknowledged=%d live_queue=%d congested=%v reason=%v",
		st.GeneratedFixes, st.AcknowledgedFixes, st.LiveQueueCount, st.Congested, st.CongestionReason)
}

// TestOverload_AdequateRateEventuallyDrains implements the spec's
// complementary claim: "At adequate rate, finite-route EOF must eventually
// drain if the aggregator responds successfully." 0.3 packets/simulated
// second at 1000x multiplier is 300/s actual, comfortably above the ~100/s
// demand implied by a 10-simulated-second report interval at 1 Hz.
func TestOverload_AdequateRateEventuallyDrains(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 1000
		c.PacketSize = 10
		c.NormalReportIntervalSimulatedMs = 10000
		c.MaxPacketsPerSimulatedSecond = 0.3 // *1000 -> 300/s actual: adequate per spec's guidance
	}, buildRouteCSV(2000)) // finite, modest route so EOF is reached quickly

	e.Start()
	for i := 0; i < 5000 && e.Status().PlaybackState != PlaybackCompleted; i++ {
		deps.mono.Advance(1 * time.Millisecond)
		e.Tick()
		time.Sleep(50 * time.Microsecond)
	}

	st := e.Status()
	if st.PlaybackState != PlaybackCompleted {
		t.Fatalf("expected route to fully drain to COMPLETED with adequate rate, got %s (unsent=%d)", st.PlaybackState, st.TotalUnsentFixes)
	}
	if st.GeneratedFixes != st.AcknowledgedFixes {
		t.Fatalf("expected all generated fixes acknowledged once drained, generated=%d acknowledged=%d", st.GeneratedFixes, st.AcknowledgedFixes)
	}
}
