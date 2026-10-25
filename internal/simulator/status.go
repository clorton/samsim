// This file implements SIM-06's GET /status: a coherent snapshot of all
// engine state relevant to an operator or the aggregator's control proxy.
package simulator

import "time"

// ConfigSnapshot mirrors the subset of Config exposed in GET /status's
// "config" object (SIM-06). listen/simulator_id/identity_db_path/routes map
// and resource limits are startup-only and intentionally omitted, per "do
// not expose filesystem paths or credentials."
type ConfigSnapshot struct {
	RouteID                         string   `json:"route_id"`
	RunStartMs                      RunStart `json:"run_start_ms"`
	ClockMultiplier                 float64  `json:"clock_multiplier"`
	ExpectedFixIntervalMs           int64    `json:"expected_fix_interval_ms"`
	NormalReportIntervalSimulatedMs int64    `json:"normal_report_interval_simulated_ms"`
	PacketSize                      int      `json:"packet_size"`
	Encoding                        string   `json:"encoding"`
	MaxPacketsPerSimulatedSecond    float64  `json:"max_packets_per_simulated_second"`
	LiveWeight                      int      `json:"live_weight"`
	BacklogWeight                   int      `json:"backlog_weight"`
	RequestTimeoutMs                int64    `json:"request_timeout_ms"`
}

// Status is the full GET /status response body (SIM-06).
type Status struct {
	SimulatorID       string            `json:"simulator_id"`
	VehicleID         string            `json:"vehicle_id"`
	RunID             *string           `json:"run_id"`
	ControlRevision   int64             `json:"control_revision"`
	PlaybackState     PlaybackState     `json:"playback_state"`
	TransmissionState TransmissionState `json:"transmission_state"`
	CoveragePaused    bool              `json:"coverage_paused"`
	Config            ConfigSnapshot    `json:"config"`
	AvailableRouteIDs []string          `json:"available_route_ids"`

	ResolvedRunStartMs *int64 `json:"resolved_run_start_ms"`
	RouteRows          int    `json:"route_rows"`
	GeneratedFixes     int64  `json:"generated_fixes"`
	AcknowledgedFixes  int64  `json:"acknowledged_fixes"`
	DiscardedFixes     int64  `json:"discarded_fixes"`
	QuarantinedFixes   int64  `json:"quarantined_fixes"`

	LiveQueueCount            int   `json:"live_queue_count"`
	BacklogQueueCount         int   `json:"backlog_queue_count"`
	LiveWindowPendingCount    int   `json:"live_window_pending_count"`
	BacklogWindowPendingCount int   `json:"backlog_window_pending_count"`
	PendingFixCount           int   `json:"pending_fix_count"`
	TotalUnsentFixes          int64 `json:"total_unsent_fixes"`

	PendingPacketID *string `json:"pending_packet_id"`
	InFlight        bool    `json:"in_flight"`
	PendingFailed   bool    `json:"pending_failed"`

	// FreshFirstPending reports whether a one-shot, non-blocking preference
	// for a live report is currently armed (set by resume-transmission when
	// backlog is pending); unlike the prior recovery_waiting_for_live gate,
	// this never blocks backlog sends (SIMULATOR_CHANGE_REQUEST.md).
	FreshFirstPending bool `json:"fresh_first_pending"`

	// NextReportWindowCloseVirtualMs is the virtual-elapsed-ms boundary at
	// which the currently open report window will close and become
	// eligible to send (SIMULATOR_CHANGE_REQUEST.md "next report
	// deadline"). Meaningful only while a run has been started.
	NextReportWindowCloseVirtualMs *int64 `json:"next_report_window_close_virtual_ms"`

	// ExpectedFixesPerReport is a derived estimate (normal_report_interval_
	// simulated_ms / expected_fix_interval_ms) of how many fixes an
	// ordinary report window should normally contain, per
	// SIMULATOR_CHANGE_REQUEST.md's "Show expected fixes per report as a
	// derived estimate."
	ExpectedFixesPerReport float64 `json:"expected_fixes_per_report"`

	// ActualReportIntervalMs and ActualMaxPacketsPerSecond are the
	// multiplier-scaled wall-clock figures derived from the simulated-time
	// configuration: actual_report_interval_ms = normal_report_interval_
	// simulated_ms / clock_multiplier; actual_max_packets_per_second =
	// max_packets_per_simulated_second * clock_multiplier.
	ActualReportIntervalMs    float64 `json:"actual_report_interval_ms"`
	ActualMaxPacketsPerSecond float64 `json:"actual_max_packets_per_second"`

	LatestGeneratedTimestampMs    *int64 `json:"latest_generated_timestamp_ms"`
	LatestAcknowledgedTimestampMs *int64 `json:"latest_acknowledged_timestamp_ms"`
	VirtualElapsedMs              int64  `json:"virtual_elapsed_ms"`
	OldestUnsentTimestampMs       *int64 `json:"oldest_unsent_timestamp_ms"`
	OldestLiveAgeWallMs           *int64 `json:"oldest_live_age_wall_ms"`

	PacketsAttempted    int64 `json:"packets_attempted"`
	PacketsAcknowledged int64 `json:"packets_acknowledged"`

	GeneratedFixesPerSecond    float64 `json:"generated_fixes_per_second"`
	AcknowledgedFixesPerSecond float64 `json:"acknowledged_fixes_per_second"`

	Congested        bool    `json:"congested"`
	CongestionReason *string `json:"congestion_reason"`

	// TargetPacketsPerSecond mirrors ActualMaxPacketsPerSecond (the
	// configured, multiplier-scaled ceiling); ObservedPacketsPerSecond is
	// the measured attempted-send rate over the rolling window, so target
	// and observed throughput can be compared directly (SIMULATOR_CHANGE_
	// REQUEST.md: "expose target and observed rates separately").
	TargetPacketsPerSecond   float64 `json:"target_packets_per_second"`
	ObservedPacketsPerSecond float64 `json:"observed_packets_per_second"`

	LastError *LastErrorInfo `json:"last_error"`
}

// Status returns a coherent snapshot of the engine's current state.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.mono.Now()

	multiplier := e.vclock.Multiplier()
	actualReportIntervalMs := float64(e.cfg.NormalReportIntervalSimulatedMs) / multiplier
	actualMaxPacketsPerSecond := e.cfg.MaxPacketsPerSimulatedSecond * multiplier
	var expectedFixesPerReport float64
	if e.cfg.ExpectedFixIntervalMs > 0 {
		expectedFixesPerReport = float64(e.cfg.NormalReportIntervalSimulatedMs) / float64(e.cfg.ExpectedFixIntervalMs)
	}

	s := Status{
		SimulatorID:       e.cfg.SimulatorID,
		VehicleID:         e.cfg.VehicleID,
		ControlRevision:   e.controlRevision,
		PlaybackState:     e.playbackState,
		TransmissionState: e.transmissionStateLocked(),
		CoveragePaused:    e.coveragePaused,
		Config: ConfigSnapshot{
			RouteID:                         e.cfg.RouteID,
			RunStartMs:                      e.cfg.RunStartMs,
			ClockMultiplier:                 multiplier,
			ExpectedFixIntervalMs:           e.cfg.ExpectedFixIntervalMs,
			NormalReportIntervalSimulatedMs: e.cfg.NormalReportIntervalSimulatedMs,
			PacketSize:                      e.cfg.PacketSize,
			Encoding:                        e.cfg.Encoding,
			MaxPacketsPerSimulatedSecond:    e.cfg.MaxPacketsPerSimulatedSecond,
			LiveWeight:                      e.cfg.LiveWeight,
			BacklogWeight:                   e.cfg.BacklogWeight,
			RequestTimeoutMs:                e.cfg.RequestTimeoutMs,
		},
		RouteRows:                 len(e.route),
		GeneratedFixes:            e.generatedFixes,
		AcknowledgedFixes:         e.acknowledgedFixesCount,
		DiscardedFixes:            e.discardedFixesCount,
		QuarantinedFixes:          e.quarantinedFixesCount,
		LiveQueueCount:            len(e.liveQueue),
		BacklogQueueCount:         len(e.backlogQueue),
		LiveWindowPendingCount:    len(e.liveWindowAccum),
		BacklogWindowPendingCount: len(e.backlogWindowAccum),
		FreshFirstPending:         e.freshFirstPending,
		ExpectedFixesPerReport:    expectedFixesPerReport,
		ActualReportIntervalMs:    actualReportIntervalMs,
		ActualMaxPacketsPerSecond: actualMaxPacketsPerSecond,
		TargetPacketsPerSecond:    actualMaxPacketsPerSecond,
		VirtualElapsedMs:          e.vclock.VirtualElapsedMs(),
		PacketsAttempted:          e.packetsAttempted,
		PacketsAcknowledged:       e.packetsAcknowledged,
	}
	if e.runStartResolved {
		v := e.nextWindowCloseMs
		s.NextReportWindowCloseVirtualMs = &v
	}

	for id := range e.routeIDsLocked() {
		s.AvailableRouteIDs = append(s.AvailableRouteIDs, id)
	}

	if e.runID != "" {
		runID := e.runID
		s.RunID = &runID
	}
	if e.runStartResolved {
		v := e.resolvedRunStartMs
		s.ResolvedRunStartMs = &v
	}
	if e.latestGeneratedTimestampMs > 0 {
		v := e.latestGeneratedTimestampMs
		s.LatestGeneratedTimestampMs = &v
	}
	if e.latestAcknowledgedTimestampMs > 0 {
		v := e.latestAcknowledgedTimestampMs
		s.LatestAcknowledgedTimestampMs = &v
	}

	if e.pending != nil {
		s.PendingFixCount = len(e.pending.recordIndices)
		s.InFlight = e.pending.inFlight
		s.PendingFailed = e.pending.failed || e.pending.identityFailure
		if e.pending.packet != nil {
			id := e.pending.packet.PacketID
			s.PendingPacketID = &id
		}
	}
	s.TotalUnsentFixes = int64(e.totalUnsentLocked())

	if oldestTs, ok := e.oldestUnsentTimestampLocked(); ok {
		s.OldestUnsentTimestampMs = &oldestTs
	}
	if len(e.liveQueue) > 0 {
		age := now.Sub(e.liveQueue[0].enqueuedAt).Milliseconds()
		s.OldestLiveAgeWallMs = &age
	}

	s.GeneratedFixesPerSecond = rollingRate(e.generatedSamples, e.generatedFixes, now)
	s.AcknowledgedFixesPerSecond = rollingRate(e.acknowledgedSamples, e.acknowledgedFixesCount, now)
	s.ObservedPacketsPerSecond = rollingRate(e.attemptedSamples, e.packetsAttempted, now)

	s.Congested, s.CongestionReason = e.congestionLocked(now)

	s.LastError = e.lastError

	return s
}

// transmissionStateLocked derives TransmissionState from pending/coverage
// state (SIM-02/06). Caller must hold e.mu.
func (e *Engine) transmissionStateLocked() TransmissionState {
	if e.pending != nil && (e.pending.failed || e.pending.identityFailure) {
		return TransmissionBlockedError
	}
	if e.coveragePaused {
		return TransmissionPaused
	}
	return TransmissionEnabled
}

// oldestUnsentTimestampLocked returns the event timestamp of the oldest
// record across live, backlog and pending (whichever is earliest), per
// status's oldest_unsent_timestamp_ms. Caller must hold e.mu.
func (e *Engine) oldestUnsentTimestampLocked() (int64, bool) {
	best := int64(0)
	found := false
	consider := func(ts int64) {
		if !found || ts < best {
			best = ts
			found = true
		}
	}
	if len(e.liveQueue) > 0 {
		consider(e.route[e.liveQueue[0].routeIndex].TimestampMs)
	}
	if len(e.backlogQueue) > 0 {
		consider(e.route[e.backlogQueue[0].routeIndex].TimestampMs)
	}
	if len(e.liveWindowAccum) > 0 {
		consider(e.route[e.liveWindowAccum[0].routeIndex].TimestampMs)
	}
	if len(e.backlogWindowAccum) > 0 {
		consider(e.route[e.backlogWindowAccum[0].routeIndex].TimestampMs)
	}
	if e.pending != nil && len(e.pending.recordIndices) > 0 {
		consider(e.route[e.pending.recordIndices[0]].TimestampMs)
	}
	return best, found
}

// congestionLocked implements SIM-05's congestion diagnostic: congested
// when enabled/running and the oldest live queue age exceeds 1000 wall ms,
// or observed queue growth persists across the rolling window. Caller must
// hold e.mu.
func (e *Engine) congestionLocked(now time.Time) (bool, *string) {
	if e.playbackState != PlaybackRunning && e.playbackState != PlaybackDraining {
		return false, nil
	}
	if e.coveragePaused || (e.pending != nil && (e.pending.failed || e.pending.identityFailure)) {
		return false, nil
	}
	if len(e.liveQueue) > 0 {
		age := now.Sub(e.liveQueue[0].enqueuedAt)
		if age > time.Second {
			reason := "oldest_live_queue_age_exceeds_1000ms"
			return true, &reason
		}
	}
	if e.generatedFixesPerSecondExceedsAckLocked(now) {
		reason := "sustained_queue_growth"
		return true, &reason
	}
	return false, nil
}

// generatedFixesPerSecondExceedsAckLocked reports whether generation has
// sustained outpacing acknowledgment across the whole rolling window
// (i.e. both sample sets span close to the full window, indicating a
// persistent trend rather than a brief transient). Caller must hold e.mu.
func (e *Engine) generatedFixesPerSecondExceedsAckLocked(now time.Time) bool {
	if len(e.generatedSamples) == 0 {
		return false
	}
	span := now.Sub(e.generatedSamples[0].atMono)
	if span < rollingWindow-time.Second {
		return false // not enough history yet to call this "persistent"
	}
	genRate := rollingRate(e.generatedSamples, e.generatedFixes, now)
	ackRate := rollingRate(e.acknowledgedSamples, e.acknowledgedFixesCount, now)
	return genRate > ackRate+1e-9
}

// routeIDsLocked returns the configured route ID map's keys as a set.
// Caller must hold e.mu.
func (e *Engine) routeIDsLocked() map[string]struct{} {
	out := make(map[string]struct{}, len(e.cfg.Routes))
	for id := range e.cfg.Routes {
		out[id] = struct{}{}
	}
	return out
}
