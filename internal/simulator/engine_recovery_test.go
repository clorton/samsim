package simulator

import (
	"testing"
	"time"
)

// recoveryTestConfig sets a normal report interval far larger than any
// virtual-time span these tests advance through, so nothing becomes
// eligible to send purely by crossing an ordinary window boundary; only
// EOF sealing or the fresh-first preference's explicit "even if partial"
// bypass can close a window during these tests. This lets each test
// control exactly when sends occur by controlling virtual-time advances
// and pause/resume alone, independent of packet_size.
func recoveryTestConfig(c *Config) {
	c.PacketSize = 1000
	c.NormalReportIntervalSimulatedMs = 999_000_000
}

// TestEngine_PauseMovesLiveToBacklog implements part of S08: pausing
// coverage atomically moves all unsent live entries (both already-closed
// queue records and records still accumulating in the open window) to
// backlog, preserving order.
func TestEngine_PauseMovesLiveToBacklog(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		recoveryTestConfig(c)
		c.ClockMultiplier = 10
	}, buildRouteCSV(1000))
	e.Start()
	deps.mono.Advance(500 * time.Millisecond) // *10 -> 5 virtual seconds -> ~5 rows
	e.Tick()

	before := e.Status()
	if before.LiveWindowPendingCount == 0 {
		t.Fatal("expected nonzero live window-pending count before pause")
	}
	if before.LiveQueueCount != 0 {
		t.Fatalf("expected no closed live queue entries yet (window interval never closes in this test), got %d", before.LiveQueueCount)
	}

	e.PauseTransmission()

	after := e.Status()
	if after.LiveQueueCount != 0 || after.LiveWindowPendingCount != 0 {
		t.Fatalf("expected live queue and window-pending both emptied after pause, got queue=%d pending=%d", after.LiveQueueCount, after.LiveWindowPendingCount)
	}
	if after.BacklogWindowPendingCount != before.LiveWindowPendingCount {
		t.Fatalf("expected all live window-pending records moved to backlog window-pending: before live=%d, after backlog=%d", before.LiveWindowPendingCount, after.BacklogWindowPendingCount)
	}
	if after.TransmissionState != TransmissionPaused {
		t.Fatalf("expected PAUSED, got %s", after.TransmissionState)
	}
	if deps.sender.sentCount() != 0 {
		t.Fatalf("expected no sends (report window never closes in this test), got %d", deps.sender.sentCount())
	}
}

// TestEngine_PauseNoNewAttempts implements part of S08: no new HTTP
// attempts start after pause takes effect.
func TestEngine_PauseNoNewAttempts(t *testing.T) {
	e, deps := newTestEngine(t, recoveryTestConfig, buildRouteCSV(1000))
	e.Start()
	e.PauseTransmission()
	deps.mono.Advance(5 * time.Second)
	e.Tick()
	e.Tick()
	if deps.sender.sentCount() != 0 {
		t.Fatalf("expected no sends while paused, got %d", deps.sender.sentCount())
	}
}

// TestEngine_FreshFirstPreference_NeverBlocksBacklog implements the
// SIMULATOR_CHANGE_REQUEST.md replacement for the old recovery gate: once
// backlog is closed/eligible, it may be sent immediately on resume even
// though no live report has arrived yet -- "Never wait for another normal
// report deadline when backlog is eligible and no live report is ready."
func TestEngine_FreshFirstPreference_NeverBlocksBacklog(t *testing.T) {
	e, deps := newTestEngine(t, recoveryTestConfig, buildRouteCSV(10000))

	e.Start()
	deps.mono.Advance(300 * time.Millisecond) // generate some live records into the (never-closing) open window
	e.Tick()
	if e.Status().LiveWindowPendingCount == 0 {
		t.Fatal("expected some live records accumulated before pause")
	}

	e.PauseTransmission() // moves the open live window into the open backlog window
	if e.Status().BacklogWindowPendingCount == 0 {
		t.Fatal("expected the open window to have moved into backlog's open window on pause")
	}

	// Deterministically close the backlog's open window without depending
	// on virtual-time boundary arithmetic: stop playback, shrink the report
	// interval drastically (this forces the current open window closed as a
	// side effect, per PutConfig's interval-change handling), then resume
	// playback. The new interval also guarantees everything closes promptly
	// from here on.
	e.Stop()
	tinyInterval := int64(1)
	if err := e.PutConfig(ConfigUpdate{NormalReportIntervalSimulatedMs: &tinyInterval}); err != nil {
		t.Fatal(err)
	}
	if e.Status().BacklogQueueCount == 0 {
		t.Fatal("expected the interval change to seal the open backlog window into the ready queue")
	}
	e.Start()

	e.ResumeTransmission(ResumeTransmissionOptions{})
	if !e.Status().FreshFirstPending {
		t.Fatal("expected fresh_first_pending=true immediately after resume with backlog and future points")
	}

	// No live fix has been generated post-resume, but backlog is already
	// closed/eligible: a send must proceed immediately, choosing backlog,
	// rather than blocking on a future live fix.
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1)
	if deps.sender.sentCount() != 1 {
		t.Fatalf("expected backlog to be sent immediately despite the armed fresh-first preference, got %d sends", deps.sender.sentCount())
	}
	if !e.Status().FreshFirstPending {
		t.Fatal("expected fresh_first_pending to remain armed: it was not satisfied by a live send yet")
	}
}

// TestEngine_FreshFirstPreference_PrefersLiveOnceAvailable implements the
// "one immediate live report may contain the latest reporting window, even
// if partial" half of SIMULATOR_CHANGE_REQUEST.md's fresh-first rule: once
// a live fix is available post-resume, the next attempt force-closes and
// sends it ahead of the normal window boundary, consuming the preference.
func TestEngine_FreshFirstPreference_PrefersLiveOnceAvailable(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		recoveryTestConfig(c)
		c.ClockMultiplier = 10
	}, buildRouteCSV(10000))

	e.Start()
	deps.mono.Advance(500 * time.Millisecond) // 5 virtual seconds -> ~5 rows into the (never-closing) live window
	e.Tick()
	if e.Status().LiveWindowPendingCount == 0 {
		t.Fatal("expected some live records generated before pause")
	}

	e.PauseTransmission() // moves accumulated live window -> backlog window
	if e.Status().FreshFirstPending {
		t.Fatal("expected no preference armed until resume")
	}

	// Advance virtual time by >600s while paused: route points continue
	// generating, but into backlog since coveragePaused is still true.
	deps.mono.Advance(70 * time.Second) // *10 -> 700 virtual seconds
	e.Tick()
	backlogPendingDuringOutage := e.Status().BacklogWindowPendingCount
	if backlogPendingDuringOutage == 0 {
		t.Fatal("expected backlog window-pending to keep growing during outage")
	}
	if deps.sender.sentCount() != 0 {
		t.Fatalf("expected no sends while still paused, got %d", deps.sender.sentCount())
	}

	e.ResumeTransmission(ResumeTransmissionOptions{})
	if !e.Status().FreshFirstPending {
		t.Fatal("expected fresh_first_pending=true immediately after resume with backlog and future points")
	}

	// Nothing has closed yet (recoveryTestConfig's interval never closes
	// naturally, and backlog's window hasn't been force-closed), so no
	// send should occur yet.
	e.Tick()
	if deps.sender.sentCount() != 0 {
		t.Fatalf("expected no send before a post-outage live fix is generated, got %d", deps.sender.sentCount())
	}

	// Advance a little more virtual time so a new live fix generates.
	deps.mono.Advance(1 * time.Second) // +10 virtual seconds -> new live record(s)
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1)

	st := e.Status()
	if st.FreshFirstPending {
		t.Fatal("expected fresh_first_pending cleared after the first post-outage live send")
	}
	if st.LiveQueueCount != 0 || st.LiveWindowPendingCount != 0 {
		t.Fatalf("expected the fresh-first live send to drain all available live records, got queue=%d pending=%d", st.LiveQueueCount, st.LiveWindowPendingCount)
	}
	if st.BacklogWindowPendingCount != backlogPendingDuringOutage {
		t.Fatalf("expected backlog untouched by the fresh-first live send: before=%d after=%d", backlogPendingDuringOutage, st.BacklogWindowPendingCount)
	}
}

// TestEngine_FreshFirstPreference_EOFClearsWithoutBlocking implements: if
// EOF is already reached while paused, resuming drains backlog immediately
// without ever waiting for nonexistent future live data, and the
// preference is cleared rather than left armed forever.
func TestEngine_FreshFirstPreference_EOFClearsWithoutBlocking(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 10
		c.PacketSize = 1000
		c.NormalReportIntervalSimulatedMs = 500 // closes promptly so backlog is eligible once paused
	}, buildRouteCSV(20))

	e.Start()
	deps.mono.Advance(200 * time.Millisecond) // 2 virtual seconds -> a few rows into live
	e.Tick()
	if e.Status().LiveQueueCount == 0 && e.Status().LiveWindowPendingCount == 0 {
		t.Fatal("expected some live records before pause")
	}

	e.PauseTransmission() // moves live -> backlog

	// Consume the rest of the route while still paused -> reaches DRAINING
	// with everything landing in backlog (none in live, since paused). EOF
	// force-closes the open window per generateDueLocked.
	deps.mono.Advance(10 * time.Second) // *10 -> 100 virtual seconds, more than enough for 20 rows total
	e.Tick()
	if e.Status().PlaybackState != PlaybackDraining {
		t.Fatalf("expected DRAINING once route exhausted, got %s", e.Status().PlaybackState)
	}
	if deps.sender.sentCount() != 0 {
		t.Fatalf("expected no sends while still paused, even in DRAINING, got %d", deps.sender.sentCount())
	}

	e.ResumeTransmission(ResumeTransmissionOptions{})

	// EOF already reached, so no live will ever come; backlog must drain
	// immediately without the preference blocking it.
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1)
	if e.Status().FreshFirstPending {
		t.Fatal("expected fresh-first preference cleared immediately when EOF already reached")
	}
}

// TestEngine_RepeatedResumeDoesNotResetPreference implements: "A repeated
// resume must not keep resetting this [preference]."
func TestEngine_RepeatedResumeDoesNotResetPreference(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		recoveryTestConfig(c)
		c.ClockMultiplier = 10
	}, buildRouteCSV(10000))

	e.Start()
	deps.mono.Advance(500 * time.Millisecond)
	e.Tick()
	e.PauseTransmission()
	deps.mono.Advance(70 * time.Second)
	e.Tick()

	e.ResumeTransmission(ResumeTransmissionOptions{})
	if !e.Status().FreshFirstPending {
		t.Fatal("expected preference armed after first resume")
	}

	e.Tick()
	if deps.sender.sentCount() != 0 {
		t.Fatal("expected no sends before a post-outage live fix generates (nothing closed yet)")
	}

	// A redundant resume call must not disturb the already-armed
	// preference (ResumeTransmission only (re-)arms it while transitioning
	// out of coveragePaused, which is already false on this second call).
	e.ResumeTransmission(ResumeTransmissionOptions{})
	if !e.Status().FreshFirstPending {
		t.Fatal("expected preference to remain armed across a redundant resume call")
	}
	if deps.sender.sentCount() != 0 {
		t.Fatal("expected still no sends after the redundant resume")
	}
}

// TestEngine_PauseWithoutBacklogNeedsNoPreference implements: "If pause
// occurred without generating any backlog, no [preference] is necessary."
func TestEngine_PauseWithoutBacklogNeedsNoPreference(t *testing.T) {
	e, deps := newTestEngine(t, recoveryTestConfig, buildRouteCSV(1000))
	e.Start()
	// Pause immediately, before any fixes have been generated at all.
	e.PauseTransmission()
	if e.Status().FreshFirstPending {
		t.Fatal("expected no preference armed when pause occurred with nothing queued")
	}
	e.ResumeTransmission(ResumeTransmissionOptions{})
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	if e.Status().LiveWindowPendingCount == 0 {
		t.Fatal("expected live records to have generated")
	}
	if e.Status().FreshFirstPending {
		t.Fatal("expected preference to remain clear with nothing having been paused-with-backlog")
	}
}

// TestEngine_FairnessGuaranteesBacklogOpportunity implements S10: with
// both lanes continuously eligible, backlog receives >=1 of every 4
// ordinary attempts (default weights). Lane usage per send is inferred by
// observing which queue's length decreases around each settled send.
func TestEngine_FairnessGuaranteesBacklogOpportunity(t *testing.T) {
	// Use a report interval large enough during setup that nothing closes
	// via the ordinary boundary path; only the fresh-first preference's
	// explicit "even if partial" bypass triggers a send during setup. This
	// keeps the fairness cycle untouched until the observation loop below,
	// which switches to a small report interval so steady-state windows
	// close every attempt.
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 1000
		recoveryTestConfig(c)
		c.MaxPacketsPerSimulatedSecond = 10 // *1000 multiplier -> 10000/s actual, 100us interval, see loop comment below
	}, buildRouteCSV(10_000))

	e.Start()
	// Generate a large pool of live data, then pause to build an even
	// LARGER backlog pool, then resume and let the fresh-first preference
	// clear with a small live trickle. Both pools are sized far larger than
	// the observation loop below can possibly consume (16 attempts * packet
	// size 5 = 80 records at most), so neither lane can starve mid-loop and
	// fall back to inconsistent eligibility.
	deps.mono.Advance(500 * time.Millisecond) // *1000 -> 500 virtual seconds -> 500 live rows
	e.Tick()
	if deps.sender.sentCount() != 0 {
		t.Fatalf("expected no sends yet (report window never closes), got %d", deps.sender.sentCount())
	}
	e.PauseTransmission()
	deps.mono.Advance(5000 * time.Millisecond) // *1000 -> 5000 more virtual seconds into backlog
	e.Tick()
	e.ResumeTransmission(ResumeTransmissionOptions{})
	deps.mono.Advance(10 * time.Millisecond) // small trickle to clear the preference
	e.Tick()
	waitForSendAndSettle(t, e, deps, 1) // consume the fresh-first live send (the only send so far)

	// Now switch to a small report interval and packet_size for the
	// steady-state fairness observation below. Playback must be STOPPED to
	// change the report interval.
	e.Stop()
	newPacketSize := 5
	newInterval := int64(1) // 1 simulated ms: closes essentially every generated row
	if err := e.PutConfig(ConfigUpdate{PacketSize: &newPacketSize, NormalReportIntervalSimulatedMs: &newInterval}); err != nil {
		t.Fatal(err)
	}
	e.Start()

	const numAttempts = 16
	var lanesSeen []Lane
	for iterations := 0; len(lanesSeen) < numAttempts && iterations < 10000; iterations++ {
		// Advance just enough to clear the ~100us rate-limit interval, but
		// deliberately not enough to generate any meaningful number of new
		// route rows (route points are 1 full second apart, so a fraction of
		// a millisecond of monotonic time -- even at 1000x multiplier, well
		// under 1 virtual second -- generates nothing new). This decouples
		// rate pacing from pool replenishment so both pools stay large and
		// count-eligible for the whole loop.
		deps.mono.Advance(150 * time.Microsecond)
		countBefore := e.selectionCount()
		e.Tick()
		if e.selectionCount() == countBefore {
			continue // no attempt dispatched this tick (rate limited or nothing eligible yet)
		}
		lane, ok := e.lastSelectedLane()
		if !ok {
			t.Fatal("expected a selected lane to be recorded for the dispatched attempt")
		}
		lanesSeen = append(lanesSeen, lane)
		waitForPendingCleared(t, e)
	}
	if len(lanesSeen) < numAttempts {
		t.Fatalf("failed to observe %d lane selections (got %d): %v", numAttempts, len(lanesSeen), lanesSeen)
	}

	for i := 0; i+3 < len(lanesSeen); i++ {
		window := lanesSeen[i : i+4]
		backlogCount := 0
		for _, l := range window {
			if l == LaneBacklog {
				backlogCount++
			}
		}
		if backlogCount == 0 {
			t.Fatalf("window %v (starting at attempt %d) has no backlog opportunity; full sequence: %v", window, i, lanesSeen)
		}
	}
}
