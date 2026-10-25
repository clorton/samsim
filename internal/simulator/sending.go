// This file implements SIM-03/04/05's packet selection, identity
// allocation, and send dispatch, as amended by SIMULATOR_CHANGE_REQUEST.md.
// prepareSendAttemptLocked (called under e.mu from Tick) decides whether an
// attempt should be made right now, applying the fresh-first preference,
// fairness selection, lane eligibility, rate limiting, and the
// single-in-flight rule, and if so selects records and marks them pending.
// The actual identity allocation and HTTP POST happen in a separate
// goroutine (dispatchSend) without holding e.mu, per SIM-06's "do not hold
// the state owner while doing network or file I/O"; Tick launches that
// goroutine and returns immediately so generation and control
// responsiveness are never blocked by a slow/stuck send.
package simulator

import (
	"context"
	"fmt"
)

// sendAttempt is the off-lock work item produced by prepareSendAttemptLocked.
type sendAttempt struct {
	isRetryOfExistingPacket bool // true: resend pending.packet unchanged; false: allocate ID and build a new packet
	fixes                   []RouteRecord
	lane                    Lane
}

// prepareSendAttemptLocked decides whether to begin a send attempt right
// now and, if so, prepares it (selecting/removing records from the queue
// for a new attempt, or identifying an existing pending packet to retry),
// marking e.pending.inFlight so no second attempt can be prepared
// concurrently. Returns nil if no attempt should start right now. Caller
// must hold e.mu; this method never performs I/O.
func (e *Engine) prepareSendAttemptLocked() *sendAttempt {
	if e.pending != nil && e.pending.inFlight {
		return nil // single in-flight rule
	}
	if e.playbackState != PlaybackRunning && e.playbackState != PlaybackDraining {
		return nil // Stop (or not yet started) prevents sending regardless of mode
	}

	if e.pending != nil && (e.pending.failed || e.pending.identityFailure) {
		if !e.pending.retryAuthorized {
			return nil // blocked until explicit resume-transmission retry
		}
		if !e.rateAllowsAttemptLocked() {
			return nil
		}
		e.markAttemptStartedLocked()
		if e.pending.identityFailure {
			fixes := make([]RouteRecord, len(e.pending.recordIndices))
			for i, idx := range e.pending.recordIndices {
				fixes[i] = e.route[idx]
			}
			return &sendAttempt{isRetryOfExistingPacket: false, fixes: fixes, lane: e.pending.lane}
		}
		return &sendAttempt{isRetryOfExistingPacket: true, lane: e.pending.lane}
	}

	// No pending packet: ordinary new selection.
	if e.coveragePaused {
		return nil // no new HTTP attempts start while paused
	}
	if !e.rateAllowsAttemptLocked() {
		return nil
	}

	lane, recordIdx, ok := e.selectOrdinaryLocked()
	if !ok {
		return nil
	}

	var queue *[]queuedRecord
	if lane == LaneLive {
		queue = &e.liveQueue
	} else {
		queue = &e.backlogQueue
	}
	n := e.cfg.PacketSize
	if n > len(*queue) {
		n = len(*queue)
	}
	selected := (*queue)[:n]
	*queue = (*queue)[n:]

	indices := make([]int, len(selected))
	fixes := make([]RouteRecord, len(selected))
	for i, qr := range selected {
		indices[i] = qr.routeIndex
		fixes[i] = e.route[qr.routeIndex]
	}

	e.pending = &pendingPacketState{recordIndices: indices, lane: lane}
	e.fairness.Advance()
	_ = recordIdx // reserved for future diagnostics; selection already applied above

	e.testLastSelectedLane = lane
	e.testLastSelectedLaneSet = true
	e.testSelectionCount++

	e.markAttemptStartedLocked()
	return &sendAttempt{isRetryOfExistingPacket: false, fixes: fixes, lane: lane}
}

// rateAllowsAttemptLocked enforces SIM-05's monotonic wall-clock spacing:
// attempts are at least 1/(max_packets_per_simulated_second*clock_
// multiplier) apart, anchored on the last actual attempt (no burst credit
// accumulation). Caller must hold e.mu.
func (e *Engine) rateAllowsAttemptLocked() bool {
	if !e.haveLastAttempt {
		return true
	}
	interval := e.rateIntervalLocked()
	return e.mono.Now().Sub(e.lastAttemptMonoTime) >= interval
}

// markAttemptStartedLocked records the rate-limiting anchor and, if
// e.pending already exists (retry path), marks it in flight; for a fresh
// ordinary attempt the caller sets e.pending itself immediately
// afterward/before calling this (either order is fine; this method only
// touches the anchor and, when non-nil, the in-flight flag). Caller must
// hold e.mu.
func (e *Engine) markAttemptStartedLocked() {
	e.lastAttemptMonoTime = e.mono.Now()
	e.haveLastAttempt = true
	if e.pending != nil {
		e.pending.inFlight = true
		e.pending.retryAuthorized = false
	}
	e.packetsAttempted++
	e.attemptedSamples = pruneSamples(append(e.attemptedSamples, rateSample{atMono: e.lastAttemptMonoTime, count: e.packetsAttempted}), e.lastAttemptMonoTime)
}

// selectOrdinaryLocked implements the fresh-first recovery preference plus
// weighted fairness selection for a brand-new (non-retry) ordinary attempt,
// per SIMULATOR_CHANGE_REQUEST.md. Unlike the prior recovery gate, this
// never blocks: if freshFirstPending is armed (set by ResumeTransmission)
// and live has any eligible records, this attempt prefers live once,
// consuming the one-shot preference; otherwise (live not yet ready, or the
// preference already consumed) ordinary weighted fairness decides, and
// backlog remains free to send even with nothing live available ( "Never
// wait for another normal report deadline when backlog is eligible and no
// live report is ready"). Caller must hold e.mu.
func (e *Engine) selectOrdinaryLocked() (lane Lane, recordIdx int, ok bool) {
	if e.freshFirstPending {
		// "one immediate live report may contain the latest reporting
		// window, even if partial": force-close just the open live window
		// (not backlog) so any fixes generated since resume become eligible
		// now rather than waiting for the normal boundary.
		if len(e.liveQueue) == 0 && len(e.liveWindowAccum) > 0 {
			e.liveQueue = append(e.liveQueue, e.liveWindowAccum...)
			e.liveWindowAccum = nil
		}
		if len(e.liveQueue) > 0 {
			e.freshFirstPending = false
			return LaneLive, 0, true
		}
		if !e.isLaneEligibleLocked(e.backlogQueue) {
			// Nothing eligible yet at all; leave the preference armed for a
			// later tick once something becomes available.
			return "", 0, false
		}
		// Live isn't ready yet but backlog is: per spec, never block backlog
		// on a future live fix. Fall through to ordinary fairness below. If
		// the route is already exhausted, no live record will ever arrive,
		// so consume the preference now rather than re-checking forever;
		// otherwise leave it armed so a later attempt still prefers live as
		// soon as it becomes eligible.
		if e.generatedCursor >= len(e.route) {
			e.freshFirstPending = false
		}
	}

	elig := LaneEligibility{
		LiveEligible:    e.isLaneEligibleLocked(e.liveQueue),
		BacklogEligible: e.isLaneEligibleLocked(e.backlogQueue),
	}
	selected, ok := e.fairness.Select(elig)
	if !ok {
		return "", 0, false
	}
	return selected, 0, true
}

// dispatchSend performs the off-lock work for attempt (identity allocation
// if needed, then the HTTP send) and commits the result back into engine
// state under e.mu. It is always run in its own goroutine, launched by
// Tick, so a slow/blocked aggregator never stalls generation or control
// responsiveness.
func (e *Engine) dispatchSend(attempt *sendAttempt) {
	var pkt *Packet

	if attempt.isRetryOfExistingPacket {
		e.mu.Lock()
		pkt = e.pending.packet
		e.mu.Unlock()
	} else {
		utcDate := UTCDateString(e.nowWall())
		seq, err := e.identity.ReserveNextSequence(e.cfg.SimulatorID, utcDate)
		if err != nil {
			e.commitIdentityFailure(err)
			return
		}
		packetID := FormatPacketID(utcDate, seq)

		e.mu.Lock()
		encoding := Encoding(e.cfg.Encoding)
		vehicleID := e.cfg.VehicleID
		simulatorID := e.cfg.SimulatorID
		e.mu.Unlock()

		built, err := BuildPacket(packetID, simulatorID, vehicleID, attempt.fixes, encoding)
		if err != nil {
			// Defensive: selection/build invariants should prevent this.
			// Treat as an identity-class failure (packet not yet sendable),
			// so the operator can retry after investigating.
			e.commitIdentityFailure(fmt.Errorf("build packet: %w", err))
			return
		}
		pkt = built

		e.mu.Lock()
		e.pending.packet = pkt
		e.mu.Unlock()
	}

	outcome := e.sender.Send(context.Background(), pkt)
	e.commitSendOutcome(pkt, outcome)
}

// commitIdentityFailure records a failed packet-ID allocation attempt,
// blocking further sends until an explicit retry (SIM-03/09).
func (e *Engine) commitIdentityFailure(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending.inFlight = false
	e.pending.identityFailure = true
	e.pending.attemptedAtMs = e.nowWall().UnixMilli()
	e.lastError = &LastErrorInfo{
		Kind:        ErrorKindIdentityAlloc,
		Message:     safeErrorMessage(err),
		TimestampMs: e.nowWall().UnixMilli(),
	}
}

// commitSendOutcome records the result of a completed HTTP send attempt,
// either clearing the pending packet (success) or marking it failed/blocked
// (SIM-03/04).
func (e *Engine) commitSendOutcome(pkt *Packet, outcome SendOutcome) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.mono.Now()
	e.pending.inFlight = false
	e.pending.lastOutcome = &outcome
	e.pending.attemptedAtMs = e.nowWall().UnixMilli()

	if outcome.Receipt != nil {
		n := int64(len(e.pending.recordIndices))
		e.acknowledgedFixesCount += n
		e.quarantinedFixesCount += int64(outcome.Receipt.QuarantinedFixes)
		if pkt.LastTimestampMs > e.latestAcknowledgedTimestampMs {
			e.latestAcknowledgedTimestampMs = pkt.LastTimestampMs
		}
		e.packetsAcknowledged++
		e.recordAcknowledgedSampleLocked(now)
		e.pending = nil
		e.lastError = nil
		return
	}

	e.pending.failed = true
	kind := ErrorKindNetwork
	switch outcome.FailureKind {
	case FailureKindTimeout:
		kind = ErrorKindTimeout
	case FailureKindNon200:
		kind = ErrorKindNon200
	case FailureKindMalformedReceipt:
		kind = ErrorKindMalformedReceipt
	}
	e.lastError = &LastErrorInfo{
		Kind:                kind,
		Message:             safeErrorMessage(fmt.Errorf("%s", outcome.ErrorMessage)),
		HTTPStatus:          outcome.HTTPStatus,
		AcceptanceUncertain: outcome.AcceptanceUncertain,
		PacketID:            pkt.PacketID,
		TimestampMs:         e.nowWall().UnixMilli(),
	}
}

func safeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
