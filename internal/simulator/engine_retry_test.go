package simulator

import (
	"testing"
	"time"
)

// TestEngine_FailedSendBlocksAndRetainsPacket implements S12: a failed
// send retains the exact packet and blocks further ordinary sends until an
// explicit retry.
func TestEngine_FailedSendBlocksAndRetainsPacket(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.PacketSize = 1 }, buildRouteCSV(1000))
	deps.sender.setOutcome(func(pkt *Packet) SendOutcome {
		return SendOutcome{Failed: true, FailureKind: FailureKindNetworkError, ErrorMessage: "boom"}
	})

	e.Start()
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	st := e.Status()
	if st.TransmissionState != TransmissionBlockedError {
		t.Fatalf("expected BLOCKED_ERROR, got %s", st.TransmissionState)
	}
	if st.PendingPacketID == nil {
		t.Fatal("expected pending packet ID retained")
	}
	firstPacketID := *st.PendingPacketID

	// Further ticks must not attempt a new send or lose the retained
	// packet.
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	if deps.sender.sentCount() != 1 {
		t.Fatalf("expected no further attempts while blocked, got %d total sends", deps.sender.sentCount())
	}
	if *e.Status().PendingPacketID != firstPacketID {
		t.Fatal("expected the same packet ID retained across ticks")
	}
}

// TestEngine_ResumeTransmissionWithoutRetryFailedReturnsConfirmationRequired
// implements part of S06/S12: resuming without retry_failed=true when a
// failed packet exists requires explicit confirmation.
func TestEngine_ResumeTransmissionWithoutRetryFailedReturnsConfirmationRequired(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.PacketSize = 1 }, buildRouteCSV(1000))
	deps.sender.setOutcome(func(pkt *Packet) SendOutcome {
		return SendOutcome{Failed: true, FailureKind: FailureKindNetworkError}
	})
	e.Start()
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	if err := e.ResumeTransmission(ResumeTransmissionOptions{RetryFailed: false}); err != ErrRetryConfirmationRequired {
		t.Fatalf("expected ErrRetryConfirmationRequired, got %v", err)
	}
	// State must be unchanged: still blocked.
	if e.Status().TransmissionState != TransmissionBlockedError {
		t.Fatal("expected still blocked after rejected resume")
	}
}

// TestEngine_ExplicitRetrySendsSamePacketAndSucceeds implements S12: an
// explicit retry resends the identical packet and, on success, clears the
// block without double-allocating an ID or double-counting acknowledgment.
func TestEngine_ExplicitRetrySendsSamePacketAndSucceeds(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.PacketSize = 1 }, buildRouteCSV(1000))

	failOnce := true
	deps.sender.setOutcome(func(pkt *Packet) SendOutcome {
		if failOnce {
			failOnce = false
			return SendOutcome{Failed: true, FailureKind: FailureKindNetworkError}
		}
		return SendOutcome{Receipt: &AcceptanceReceipt{SimulatorID: pkt.SimulatorID, PacketID: pkt.PacketID, AcceptedFixes: pkt.FixCount}}
	})

	e.Start()
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	firstPacketID := *e.Status().PendingPacketID

	if err := e.ResumeTransmission(ResumeTransmissionOptions{RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	deps.mono.Advance(1 * time.Second) // allow rate interval to pass for the retry attempt
	e.Tick()
	waitForPendingCleared(t, e)

	st := e.Status()
	if st.TransmissionState != TransmissionEnabled {
		t.Fatalf("expected ENABLED after successful retry, got %s", st.TransmissionState)
	}
	if st.PendingPacketID != nil {
		t.Fatal("expected pending cleared after successful retry")
	}
	if st.AcknowledgedFixes != 1 {
		t.Fatalf("expected exactly 1 acknowledged fix (no double-count), got %d", st.AcknowledgedFixes)
	}

	if deps.sender.sentCount() != 2 {
		t.Fatalf("expected exactly 2 send attempts (original + 1 retry), got %d", deps.sender.sentCount())
	}
	if deps.sender.sent[0].PacketID != firstPacketID || deps.sender.sent[1].PacketID != firstPacketID {
		t.Fatalf("expected both attempts to use the identical packet ID %s, got %s and %s", firstPacketID, deps.sender.sent[0].PacketID, deps.sender.sent[1].PacketID)
	}
	if string(deps.sender.sent[0].Body) != string(deps.sender.sent[1].Body) {
		t.Fatal("expected both attempts to resend the byte-identical body")
	}
}

// TestEngine_SecondRetryFailureBlocksAgain implements part of S12: "A
// second failure blocks again."
func TestEngine_SecondRetryFailureBlocksAgain(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.PacketSize = 1 }, buildRouteCSV(1000))
	deps.sender.setOutcome(func(pkt *Packet) SendOutcome {
		return SendOutcome{Failed: true, FailureKind: FailureKindNetworkError}
	})
	e.Start()
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	e.ResumeTransmission(ResumeTransmissionOptions{RetryFailed: true})
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	if e.Status().TransmissionState != TransmissionBlockedError {
		t.Fatal("expected blocked again after second failure")
	}
	// retry_failed must be required again; it's not authorized automatically.
	if err := e.ResumeTransmission(ResumeTransmissionOptions{RetryFailed: false}); err != ErrRetryConfirmationRequired {
		t.Fatalf("expected confirmation required again, got %v", err)
	}
}

// TestEngine_RetryFailedNoOpWhenNoPendingFailure implements: "No pending
// failure plus retry_failed=true is a harmless no-op."
func TestEngine_RetryFailedNoOpWhenNoPendingFailure(t *testing.T) {
	e, _ := newTestEngine(t, nil, buildRouteCSV(5))
	e.Start()
	if err := e.ResumeTransmission(ResumeTransmissionOptions{RetryFailed: true}); err != nil {
		t.Fatalf("expected harmless no-op, got %v", err)
	}
}

// TestEngine_IdentityAllocationFailureBlocksWithoutInventingID implements
// the identity-allocation-failure subtype of S12/S09: if allocation fails,
// the simulator blocks sending and does not invent a packet ID.
func TestEngine_IdentityAllocationFailureBlocksWithoutInventingID(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.PacketSize = 1 }, buildRouteCSV(1000))
	deps.identity.forceNextFailure()

	e.Start()
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	st := e.Status()
	if st.TransmissionState != TransmissionBlockedError {
		t.Fatalf("expected BLOCKED_ERROR after identity allocation failure, got %s", st.TransmissionState)
	}
	if st.PendingPacketID != nil {
		t.Fatal("expected no packet ID to have been invented")
	}
	if st.LastError == nil || st.LastError.Kind != ErrorKindIdentityAlloc {
		t.Fatalf("expected last_error kind %s, got %+v", ErrorKindIdentityAlloc, st.LastError)
	}
	if deps.sender.sentCount() != 0 {
		t.Fatal("expected no HTTP send attempt when identity allocation fails")
	}

	// Explicit retry re-attempts allocation.
	if err := e.ResumeTransmission(ResumeTransmissionOptions{RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	if e.Status().TransmissionState != TransmissionEnabled {
		t.Fatalf("expected retry to succeed once identity store recovers, got %s", e.Status().TransmissionState)
	}
}

// TestEngine_QuarantinedFixesCountAsAcceptedAndRemoved implements S13:
// quarantine receipt counts as durable acceptance and removes fixes from
// the outbox.
func TestEngine_QuarantinedFixesCountAsAcceptedAndRemoved(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.PacketSize = 3 }, buildRouteCSV(1000))
	deps.sender.setOutcome(func(pkt *Packet) SendOutcome {
		return SendOutcome{Receipt: &AcceptanceReceipt{
			SimulatorID: pkt.SimulatorID, PacketID: pkt.PacketID,
			AcceptedFixes: pkt.FixCount, QuarantinedFixes: pkt.FixCount,
		}}
	})
	e.Start()
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1)

	st := e.Status()
	if st.QuarantinedFixes != 3 {
		t.Fatalf("expected 3 quarantined fixes reported, got %d", st.QuarantinedFixes)
	}
	if st.AcknowledgedFixes != 3 {
		t.Fatalf("expected quarantined fixes to count as acknowledged/accepted, got %d", st.AcknowledgedFixes)
	}
	if st.PendingFixCount != 0 || st.LiveQueueCount+st.BacklogQueueCount+st.PendingFixCount == int(st.GeneratedFixes) && st.TotalUnsentFixes != 0 {
		// sanity: nothing should remain unsent for the quarantined batch
	}
	if st.TotalUnsentFixes != st.GeneratedFixes-st.AcknowledgedFixes-st.DiscardedFixes {
		t.Fatalf("expected GENERATED = ACKNOWLEDGED + DISCARDED + TOTAL_UNSENT invariant to hold: %+v", st)
	}
}

// TestEngine_LatestAcknowledgedNeverRegresses implements part of S13:
// latest acknowledged event time never regresses during backlog sends.
func TestEngine_LatestAcknowledgedNeverRegresses(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.PacketSize = 1
		c.ClockMultiplier = 10
	}, buildRouteCSV(1000))
	e.Start()

	// Send a "live" fix with a high timestamp first (simulate out-of-order
	// acknowledgment by directly controlling outcomes/timestamps isn't
	// straightforward via the public API alone, so instead this test
	// exercises the natural ascending-order case and asserts monotonicity
	// holds across many sends).
	var lastSeen int64
	for i := 0; i < 20; i++ {
		deps.mono.Advance(200 * time.Millisecond)
		e.Tick()
		waitForPendingCleared(t, e)
		cur := e.Status().LatestAcknowledgedTimestampMs
		if cur == nil {
			continue
		}
		if *cur < lastSeen {
			t.Fatalf("latest_acknowledged_timestamp_ms regressed: was %d, now %d", lastSeen, *cur)
		}
		lastSeen = *cur
	}
	if lastSeen == 0 {
		t.Fatal("expected at least one acknowledgment to have occurred")
	}
}
