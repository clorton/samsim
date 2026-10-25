// This file implements SIM-02/06's lifecycle control operations: Start,
// Stop, Reset, and PutConfig. Each acquires controlMu first (serializing
// whole control operations against each other) and performs any off-lock
// validation/parsing before briefly acquiring mu to atomically apply the
// result, per SIM-06's "validate candidates off-thread then atomically
// apply only if state/config revision still matches."
package simulator

import (
	"fmt"

	"github.com/google/uuid"
)

// ErrResetRequired is returned by Start when playback is COMPLETED.
var ErrResetRequired = fmt.Errorf("simulator: reset required before starting again")

// Start implements POST /start (SIM-02): begins or resumes playback.
func (e *Engine) Start() error {
	e.controlMu.Lock()
	defer e.controlMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	switch e.playbackState {
	case PlaybackRunning, PlaybackDraining:
		return nil // idempotent no-op
	case PlaybackCompleted:
		return ErrResetRequired
	case PlaybackStopped:
		e.vclock.Start()
		e.playbackState = PlaybackRunning
		e.controlRevision++
		return nil
	case PlaybackReady:
		runStartMs := e.resolveRunStartLocked()
		route, err := LoadRouteCSV(e.routeData, LoadRouteCSVOptions{
			ResolvedRunStartMs: runStartMs,
			MaxBytes:           e.cfg.MaxRouteBytes,
			MaxRows:            e.cfg.MaxRouteRows,
		})
		if err != nil {
			// Should not happen: routeData was already validated at
			// config/engine-construction time with a placeholder start;
			// a real failure here would only occur from an overflow at
			// the particular resolved start, which LoadRouteCSV itself
			// checks. Surface it rather than panicking.
			return fmt.Errorf("simulator: start: route invalid at resolved run start: %w", err)
		}
		e.route = route
		e.resolvedRunStartMs = runStartMs
		e.runStartResolved = true
		e.runID = uuid.NewString()
		e.generatedCursor = 0
		e.nextWindowCloseMs = e.cfg.NormalReportIntervalSimulatedMs
		e.vclock.Start()
		e.playbackState = PlaybackRunning
		e.controlRevision++
		return nil
	default:
		return fmt.Errorf("simulator: start: unexpected playback state %q", e.playbackState)
	}
}

// resolveRunStartLocked resolves run_start_ms once, per SIM-01/02: "now"
// is resolved to the current wall clock at the moment of this call; a
// fixed configured value is used as-is. Caller must hold e.mu.
func (e *Engine) resolveRunStartLocked() int64 {
	if e.cfg.RunStartMs.IsNow {
		return e.nowWall().UnixMilli()
	}
	return e.cfg.RunStartMs.FixedMs
}

// Stop implements POST /stop (SIM-02): freezes the clock and halts new
// send attempts, retaining all queues/pending packets.
func (e *Engine) Stop() {
	e.controlMu.Lock()
	defer e.controlMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	switch e.playbackState {
	case PlaybackReady, PlaybackStopped:
		return // no-op
	case PlaybackRunning:
		e.vclock.Stop()
		e.playbackState = PlaybackStopped
		e.controlRevision++
	case PlaybackDraining:
		// EOF already reached; freeze draining by treating it the same as
		// stopped generation (generatedCursor is already at len(route),
		// so there is nothing further to generate). We still mark the
		// state STOPPED to prevent new send attempts, per "Stop ...
		// prevents sending regardless of transmission mode" -- draining
		// continues to drain only while not stopped.
		e.vclock.Stop()
		e.playbackState = PlaybackStopped
		e.controlRevision++
	case PlaybackCompleted:
		return // no-op; nothing to stop
	}
}

// PauseTransmission implements POST /pause-transmission (SIM-04/06).
func (e *Engine) PauseTransmission() {
	e.controlMu.Lock()
	defer e.controlMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.coveragePaused {
		return // idempotent
	}
	e.coveragePaused = true
	if len(e.liveQueue) > 0 {
		e.backlogQueue = append(e.backlogQueue, e.liveQueue...)
		e.liveQueue = nil
	}
	// Fixes already accumulated in the currently open (not yet closed)
	// live window also become backlog-bound: once coverage is paused,
	// those fixes can no longer be delivered as a live report, and per
	// SIMULATOR_CHANGE_REQUEST.md, "pause continues fix generation into
	// backlog." They are merged into the open backlog window rather than
	// a closed queue so they still close/flush on the normal window
	// boundary, preserving "no empty reports" and window semantics.
	if len(e.liveWindowAccum) > 0 {
		e.backlogWindowAccum = append(e.backlogWindowAccum, e.liveWindowAccum...)
		e.liveWindowAccum = nil
	}
	// Whether the fresh-first preference is needed on resume depends on
	// whether backlog ends up nonempty by the time coverage is restored,
	// not merely on whether live happened to be nonempty at this exact
	// pause instant (live is often already flushed to zero by the time an
	// operator calls pause). ResumeTransmission decides and arms the
	// preference using the state visible at resume time instead.
	e.controlRevision++
}

// ResumeTransmissionOptions is the body of POST /resume-transmission.
type ResumeTransmissionOptions struct {
	RetryFailed bool
}

// ErrRetryConfirmationRequired is returned by ResumeTransmission when a
// failed packet is pending and RetryFailed was not set.
var ErrRetryConfirmationRequired = fmt.Errorf("simulator: a failed packet is pending; retry_failed must be set to true to retry it")

// ResumeTransmission implements POST /resume-transmission (SIM-04/06).
func (e *Engine) ResumeTransmission(opts ResumeTransmissionOptions) error {
	e.controlMu.Lock()
	defer e.controlMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.pending != nil && (e.pending.failed || e.pending.identityFailure) {
		if !opts.RetryFailed {
			return ErrRetryConfirmationRequired
		}
		e.pending.retryAuthorized = true
	}
	// A no-op retry_failed=true with no pending failure is harmless, per
	// spec; nothing special to do.

	// Arm the one-shot, non-blocking fresh-first preference based on state
	// at resume time (SIMULATOR_CHANGE_REQUEST.md: "if newer fixes than the
	// last acknowledged position are available, one immediate live report
	// may contain the latest reporting window, even if partial... After
	// that one recovery report, return to normal deadlines"). Unlike the
	// prior recovery gate, this never blocks backlog sends; it only
	// expresses a preference consumed by selectOrdinaryLocked's first
	// opportunity to send live. If pause occurred without generating any
	// backlog, no preference is necessary. A repeated resume must not
	// re-arm an already-armed (or already-consumed) preference.
	if e.coveragePaused {
		hasBacklog := len(e.backlogQueue) > 0 || len(e.backlogWindowAccum) > 0
		if hasBacklog && e.generatedCursor < len(e.route) {
			e.freshFirstPending = true
		}
		e.coveragePaused = false
	}
	e.controlRevision++
	return nil
}

// ErrStopRequired is returned by Reset when a send is in flight or
// playback has not been stopped.
var ErrStopRequired = fmt.Errorf("simulator: stop the run before resetting")

// ErrSendInFlight is returned by Reset when a packet send is currently in
// flight.
var ErrSendInFlight = fmt.Errorf("simulator: a packet send is currently in flight")

// ErrPendingData is returned by Reset when unsent work remains and
// discardPending was not set.
var ErrPendingData = fmt.Errorf("simulator: unsent telemetry remains; discard_pending must be set to true to discard it")

// ResetResult reports what Reset discarded.
type ResetResult struct {
	DiscardedFixes      int64
	AcceptanceUncertain bool // true if the discarded pending packet's prior send outcome was uncertain
}

// Reset implements POST /reset (SIM-02/06).
func (e *Engine) Reset(discardPending bool) (ResetResult, error) {
	e.controlMu.Lock()
	defer e.controlMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	switch e.playbackState {
	case PlaybackReady, PlaybackStopped, PlaybackCompleted:
		// allowed
	default:
		return ResetResult{}, ErrStopRequired
	}
	if e.pending != nil && e.pending.inFlight {
		return ResetResult{}, ErrSendInFlight
	}

	unsent := e.totalUnsentLocked()
	if unsent > 0 && !discardPending {
		return ResetResult{}, ErrPendingData
	}

	result := ResetResult{DiscardedFixes: int64(unsent)}
	if e.pending != nil && e.pending.lastOutcome != nil {
		result.AcceptanceUncertain = e.pending.lastOutcome.AcceptanceUncertain
	}
	if e.pending != nil && e.pending.identityFailure {
		result.AcceptanceUncertain = false
	}

	e.runID = ""
	e.generatedCursor = 0
	e.liveQueue = nil
	e.backlogQueue = nil
	e.liveWindowAccum = nil
	e.backlogWindowAccum = nil
	e.nextWindowCloseMs = 0
	e.pending = nil
	e.generatedFixes = 0
	e.acknowledgedFixesCount = 0
	e.discardedFixesCount = 0
	e.quarantinedFixesCount = 0
	e.packetsAttempted = 0
	e.packetsAcknowledged = 0
	e.latestGeneratedTimestampMs = 0
	e.latestAcknowledgedTimestampMs = 0
	e.generatedSamples = nil
	e.acknowledgedSamples = nil
	e.attemptedSamples = nil
	e.lastError = nil
	e.coveragePaused = false
	e.freshFirstPending = false
	e.runStartResolved = false
	e.resolvedRunStartMs = 0
	e.haveLastAttempt = false
	e.fairness.SetWeights(e.cfg.LiveWeight, e.cfg.BacklogWeight)
	e.vclock = NewVirtualClock(e.mono, e.cfg.ClockMultiplier)
	e.playbackState = PlaybackReady
	e.controlRevision++

	return result, nil
}
