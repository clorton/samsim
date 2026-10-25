package simulator

import (
	"testing"
	"time"
)

// TestEngine_RateLimitSpacing implements S11: attempts respect wall-clock
// spacing of at least 1/max_packets_per_second, with no burst credit
// accumulation after idle/outage.
func TestEngine_RateLimitSpacing(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 1000
		c.PacketSize = 1
		c.PartialFlushWallMs = 10
		c.MaxPacketsPerSecond = 10 // 100ms spacing
	}, buildRouteCSV(100000))

	e.Start()
	deps.mono.Advance(100 * time.Millisecond) // generate plenty of live records, well beyond idle
	e.Tick()

	// First attempt should succeed immediately (no prior anchor).
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1)

	// A second attempt right away (same instant) must NOT be allowed: must
	// wait the full 100ms interval.
	e.Tick()
	if deps.sender.sentCount() != 1 {
		t.Fatalf("expected rate limit to block an immediate second attempt, got %d sends", deps.sender.sentCount())
	}

	// Advancing by less than the interval still blocks.
	deps.mono.Advance(50 * time.Millisecond)
	e.Tick()
	if deps.sender.sentCount() != 1 {
		t.Fatalf("expected rate limit to still block at 50ms < 100ms interval, got %d sends", deps.sender.sentCount())
	}

	// Advancing past the interval allows the next attempt.
	deps.mono.Advance(60 * time.Millisecond) // total 110ms since last attempt
	e.Tick()
	waitForSendAndSettle(t, e, deps, 2)
}

// TestEngine_RateLimitNoBurstAfterIdle implements part of S11: "After an
// idle/outage period send at most one immediate packet; do not release a
// token backlog."
func TestEngine_RateLimitNoBurstAfterIdle(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 1000
		c.PacketSize = 1
		c.PartialFlushWallMs = 10
		c.MaxPacketsPerSecond = 10 // 100ms spacing
	}, buildRouteCSV(100000))

	e.Start()
	deps.mono.Advance(500 * time.Millisecond) // generate lots of live records (far more than fit in several packets)
	e.Tick()
	// This first Tick already dispatches one send immediately, since there
	// is no prior rate-limit anchor; settle it to establish the anchor
	// before simulating an idle period.
	waitForSendAndSettle(t, e, deps, 1)

	// Let a long real idle period pass without ticking (simulating the
	// process being slow to wake up / an outage in the scheduler itself),
	// then resume ticking: only ONE immediate send should be released, not
	// a burst proportional to the idle duration.
	deps.mono.Advance(10 * time.Second) // 100x the rate interval
	e.Tick()
	waitForSendAndSettle(t, e, deps, 2)

	// Immediately after that one send (no further time advance), a third
	// attempt must still be blocked by the interval -- no burst credit was
	// accumulated from the 10s idle gap.
	e.Tick()
	if deps.sender.sentCount() != 2 {
		t.Fatalf("expected exactly 1 additional send after idle (no burst); got %d total sends", deps.sender.sentCount())
	}
}

// TestEngine_RateLimitRuntimeChangeUsesLastAttemptAnchor implements part of
// S11: "Runtime rate changes apply to the next attempt using the
// last-attempt anchor."
func TestEngine_RateLimitRuntimeChangeUsesLastAttemptAnchor(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 1000
		c.PacketSize = 1
		c.PartialFlushWallMs = 10
		c.MaxPacketsPerSecond = 10 // 100ms spacing
	}, buildRouteCSV(100000))

	e.Start()
	deps.mono.Advance(500 * time.Millisecond)
	e.Tick()
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1)

	// Speed up the rate at runtime.
	newRate := 100.0 // 10ms spacing
	if err := e.PutConfig(ConfigUpdate{MaxPacketsPerSecond: &newRate}); err != nil {
		t.Fatal(err)
	}

	// The new, shorter interval is measured from the last actual attempt
	// anchor, not reset to "immediately allowed."
	deps.mono.Advance(5 * time.Millisecond) // less than new 10ms interval
	e.Tick()
	if deps.sender.sentCount() != 1 {
		t.Fatalf("expected still blocked at 5ms < new 10ms interval, got %d", deps.sender.sentCount())
	}
	deps.mono.Advance(10 * time.Millisecond) // now past new interval
	e.Tick()
	waitForSendAndSettle(t, e, deps, 2)
}

// TestEngine_SingleInFlightLimitsThroughput implements part of S11: "A
// single in-flight request further limits actual throughput."
func TestEngine_SingleInFlightLimitsThroughput(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 1000
		c.PacketSize = 1
		c.PartialFlushWallMs = 10
		c.MaxPacketsPerSecond = 10000 // effectively unlimited for this test
	}, buildRouteCSV(100000))

	block := make(chan struct{})
	deps.sender.mu.Lock()
	deps.sender.blockUntil = block
	deps.sender.mu.Unlock()

	e.Start()
	deps.mono.Advance(500 * time.Millisecond)
	e.Tick()
	e.Tick() // dispatches one send, which will block in the fake sender
	waitForSentCount(t, deps.sender, 1)

	// While the first send is blocked in flight, further ticks must not
	// start a second attempt despite abundant rate budget and eligible
	// records.
	for i := 0; i < 5; i++ {
		deps.mono.Advance(1 * time.Millisecond)
		e.Tick()
	}
	if deps.sender.sentCount() != 1 {
		t.Fatalf("expected single in-flight to block further attempts, got %d sends dispatched", deps.sender.sentCount())
	}

	close(block) // release the blocked send
	waitForSendAndSettle(t, e, deps, 1)
}
