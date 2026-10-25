package simulator

import (
	"math/rand"
	"sync/atomic"
	"testing"
	"time"
)

// assertQueueInvariant implements S15: generated = acknowledged +
// discarded + total_unsent always; no record appears in more than one
// place (checked indirectly via the counter identity, since the engine's
// own bookkeeping would have to double-count or lose a record for this
// identity to fail).
func assertQueueInvariant(t *testing.T, e *Engine) {
	t.Helper()
	st := e.Status()
	want := st.GeneratedFixes
	got := st.AcknowledgedFixes + st.DiscardedFixes + st.TotalUnsentFixes
	if want != got {
		t.Fatalf("invariant violated: generated=%d != acknowledged(%d)+discarded(%d)+unsent(%d)=%d",
			want, st.AcknowledgedFixes, st.DiscardedFixes, st.TotalUnsentFixes, got)
	}
}

// TestEngine_QueueInvariant_RandomizedOperations implements S15: run a
// randomized sequence of operations (generate, pause/resume, send
// success/failure, retry, reset) and assert the conservation invariant
// after every step.
func TestEngine_QueueInvariant_RandomizedOperations(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		r := rand.New(rand.NewSource(seed))
		e, deps := newTestEngine(t, func(c *Config) {
			c.PacketSize = 3
			c.PartialFlushWallMs = 50
			c.ClockMultiplier = 50
			c.MaxPacketsPerSecond = 1000
		}, buildRouteCSV(5000))

		var failNext atomic.Bool
		deps.sender.setOutcome(func(pkt *Packet) SendOutcome {
			if failNext.CompareAndSwap(true, false) {
				return SendOutcome{Failed: true, FailureKind: FailureKindNetworkError}
			}
			return SendOutcome{Receipt: &AcceptanceReceipt{SimulatorID: pkt.SimulatorID, PacketID: pkt.PacketID, AcceptedFixes: pkt.FixCount}}
		})

		e.Start()
		assertQueueInvariant(t, e)

		for step := 0; step < 200; step++ {
			switch r.Intn(6) {
			case 0:
				deps.mono.Advance(time.Duration(r.Intn(50)) * time.Millisecond)
				e.Tick()
			case 1:
				e.PauseTransmission()
			case 2:
				e.ResumeTransmission(ResumeTransmissionOptions{})
			case 3:
				if e.Status().TransmissionState == TransmissionBlockedError {
					e.ResumeTransmission(ResumeTransmissionOptions{RetryFailed: true})
				}
			case 4:
				if r.Intn(10) == 0 {
					failNext.Store(true)
				}
			case 5:
				deps.mono.Advance(time.Duration(r.Intn(200)) * time.Millisecond)
				e.Tick()
			}
			assertQueueInvariant(t, e)
		}

		// Drain to a stoppable state and reset, checking the invariant
		// through teardown too.
		e.Stop()
		assertQueueInvariant(t, e)
		for i := 0; i < 50 && e.Status().TotalUnsentFixes > 0; i++ {
			deps.mono.Advance(10 * time.Millisecond)
			e.Tick()
			time.Sleep(200 * time.Microsecond)
		}
		if _, err := e.Reset(true); err != nil {
			t.Fatalf("seed %d: reset: %v", seed, err)
		}
		assertQueueInvariant(t, e)
	}
}

// TestEngine_NoBacklogLossAcrossMultipleOutages implements part of S08:
// repeated pause/resume and multiple outages retain every fix (none lost,
// none duplicated in queue membership).
func TestEngine_NoBacklogLossAcrossMultipleOutages(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.PacketSize = 1000 // avoid any sends; isolate pure queue-conservation behavior
		c.PartialFlushWallMs = 999_000_000
		c.ClockMultiplier = 10
	}, buildRouteCSV(100000))

	e.Start()
	for i := 0; i < 10; i++ {
		deps.mono.Advance(50 * time.Millisecond)
		e.Tick()
		e.PauseTransmission()
		deps.mono.Advance(50 * time.Millisecond)
		e.Tick()
		e.ResumeTransmission(ResumeTransmissionOptions{})
		assertQueueInvariant(t, e)
	}

	st := e.Status()
	if st.TotalUnsentFixes != st.GeneratedFixes {
		t.Fatalf("expected all generated fixes still unsent (nothing was ever eligible to send): generated=%d unsent=%d", st.GeneratedFixes, st.TotalUnsentFixes)
	}
	if st.GeneratedFixes == 0 {
		t.Fatal("expected nonzero generated fixes across 10 outage cycles")
	}
}
