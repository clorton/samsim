// This file implements SIM-06's PUT /config: a partial, validated,
// atomically-applied update to a whitelisted subset of effective run
// fields.
package simulator

import (
	"fmt"
	"time"
)

// ConfigUpdate is a partial update to the engine's effective configuration
// (SIM-06 PUT /config). Nil fields are left unchanged. At least one
// non-nil field is required.
type ConfigUpdate struct {
	// Hot fields: applied atomically regardless of playback state. Per
	// SIMULATOR_CHANGE_REQUEST.md, "speed/capacity recompute pacing without
	// burst credits" -- MaxPacketsPerSimulatedSecond stays hot even though
	// its semantics changed (it is now a simulated-time rate scaled by
	// ClockMultiplier into the actual enforced rate).
	ClockMultiplier              *float64
	MaxPacketsPerSimulatedSecond *float64
	LiveWeight                   *int
	BacklogWeight                *int

	// Fields requiring no pending/in-flight packet.
	PacketSize       *int
	Encoding         *string
	RequestTimeoutMs *int64

	// Fields requiring READY with no pending records.
	VehicleID             *string
	RouteID               *string
	RunStartMs            *RunStart
	AggregatorURL         *string
	ExpectedFixIntervalMs *int64

	// NormalReportIntervalSimulatedMs requires STOPPED playback (not merely
	// READY-with-no-pending), per SIMULATOR_CHANGE_REQUEST.md: "interval
	// changes require stopped playback, seal the current partial window
	// once, and anchor the new schedule at current virtual time." This is a
	// distinct precondition from the ready-empty fields above: a stopped
	// run may still hold queued/pending records, which are left untouched
	// ("Do not rewrite queued packets").
	NormalReportIntervalSimulatedMs *int64

	ExpectedControlRevision *int64
}

// ErrControlRevisionConflict is returned when ExpectedControlRevision is
// supplied and stale.
var ErrControlRevisionConflict = fmt.Errorf("simulator: expected_control_revision is stale")

// ErrNoFieldsProvided is returned when a ConfigUpdate has no fields set.
var ErrNoFieldsProvided = fmt.Errorf("simulator: config update requires at least one field")

// ErrFieldRequiresNoPending is returned when packet_size/encoding/
// request_timeout_ms is changed while a pending/in-flight packet exists.
var ErrFieldRequiresNoPending = fmt.Errorf("simulator: this field cannot change while a packet is pending or in flight")

// ErrFieldRequiresReadyEmpty is returned when vehicle_id/route_id/
// run_start_ms/aggregator_url/expected_fix_interval_ms is changed outside
// READY-with-no-pending-records.
var ErrFieldRequiresReadyEmpty = fmt.Errorf("simulator: this field can only change in READY state with no pending records; reset first")

// ErrFieldRequiresStopped is returned when normal_report_interval_simulated_ms
// is changed while playback is not STOPPED (SIMULATOR_CHANGE_REQUEST.md:
// "interval changes require stopped playback").
var ErrFieldRequiresStopped = fmt.Errorf("simulator: this field can only change while playback is STOPPED")

// ErrInvalidRoute wraps a route_id change's load/validation failure, so
// HTTP handlers can map it to INVALID_ROUTE rather than the generic
// INVALID_CONFIG.
var ErrInvalidRoute = fmt.Errorf("simulator: invalid route")

func (u ConfigUpdate) isEmpty() bool {
	return u.ClockMultiplier == nil && u.MaxPacketsPerSimulatedSecond == nil && u.LiveWeight == nil &&
		u.BacklogWeight == nil && u.PacketSize == nil &&
		u.Encoding == nil && u.RequestTimeoutMs == nil && u.VehicleID == nil &&
		u.RouteID == nil && u.RunStartMs == nil && u.AggregatorURL == nil &&
		u.ExpectedFixIntervalMs == nil && u.NormalReportIntervalSimulatedMs == nil
}

// PutConfig implements PUT /config (SIM-06). It validates the entire
// update (including, for an accepted route_id change, loading/validating
// the new route off-lock) before applying any of it, then atomically
// applies all changes together.
func (e *Engine) PutConfig(update ConfigUpdate) error {
	if update.isEmpty() {
		return ErrNoFieldsProvided
	}

	e.controlMu.Lock()
	defer e.controlMu.Unlock()

	// Snapshot current config/revision for off-lock validation.
	e.mu.Lock()
	currentRevision := e.controlRevision
	currentCfg := e.cfg
	uploadedData := e.uploadedRoutes[func() string {
		if update.RouteID != nil {
			return *update.RouteID
		}
		return ""
	}()]
	playbackState := e.playbackState
	hasPending := e.pending != nil
	totalUnsent := e.totalUnsentLocked()
	e.mu.Unlock()

	if update.ExpectedControlRevision != nil && *update.ExpectedControlRevision != currentRevision {
		return ErrControlRevisionConflict
	}

	noPendingFields := update.PacketSize != nil || update.Encoding != nil || update.RequestTimeoutMs != nil
	if noPendingFields && hasPending {
		return ErrFieldRequiresNoPending
	}

	readyEmptyFields := update.VehicleID != nil || update.RouteID != nil || update.RunStartMs != nil ||
		update.AggregatorURL != nil || update.ExpectedFixIntervalMs != nil
	if readyEmptyFields {
		if playbackState != PlaybackReady {
			return ErrFieldRequiresReadyEmpty
		}
		if totalUnsent != 0 {
			return ErrFieldRequiresReadyEmpty
		}
	}

	// "Interval changes require stopped playback": treat READY (never
	// started; vacuously satisfies "not running") the same as STOPPED, so an
	// operator can configure the interval before the first Start without an
	// extra Stop() round-trip.
	if update.NormalReportIntervalSimulatedMs != nil && playbackState != PlaybackStopped && playbackState != PlaybackReady {
		return ErrFieldRequiresStopped
	}

	newCfg := currentCfg
	if update.ClockMultiplier != nil {
		newCfg.ClockMultiplier = *update.ClockMultiplier
	}
	if update.MaxPacketsPerSimulatedSecond != nil {
		newCfg.MaxPacketsPerSimulatedSecond = *update.MaxPacketsPerSimulatedSecond
	}
	if update.LiveWeight != nil {
		newCfg.LiveWeight = *update.LiveWeight
	}
	if update.BacklogWeight != nil {
		newCfg.BacklogWeight = *update.BacklogWeight
	}
	if update.NormalReportIntervalSimulatedMs != nil {
		newCfg.NormalReportIntervalSimulatedMs = *update.NormalReportIntervalSimulatedMs
	}
	if update.PacketSize != nil {
		newCfg.PacketSize = *update.PacketSize
	}
	if update.Encoding != nil {
		newCfg.Encoding = *update.Encoding
	}
	if update.RequestTimeoutMs != nil {
		newCfg.RequestTimeoutMs = *update.RequestTimeoutMs
	}
	if update.VehicleID != nil {
		newCfg.VehicleID = *update.VehicleID
	}
	if update.RouteID != nil {
		newCfg.RouteID = *update.RouteID
	}
	if update.RunStartMs != nil {
		newCfg.RunStartMs = *update.RunStartMs
	}
	if update.AggregatorURL != nil {
		newCfg.AggregatorURL = *update.AggregatorURL
	}
	if update.ExpectedFixIntervalMs != nil {
		newCfg.ExpectedFixIntervalMs = *update.ExpectedFixIntervalMs
	}

	if err := newCfg.Validate(); err != nil {
		return fmt.Errorf("simulator: config update: %w", err)
	}

	// Off-lock: load/validate a newly selected route before applying
	// anything, and build a new Sender if aggregator_url/request_timeout
	// changed (constructing an http.Client is cheap/pure, no network I/O
	// occurs here).
	var newRouteData []byte
	routeChanged := update.RouteID != nil && *update.RouteID != currentCfg.RouteID
	if routeChanged {
		if e.routeLoader == nil && uploadedData == nil {
			return fmt.Errorf("simulator: config update: route_id change requested but no route loader is configured")
		}
		data := uploadedData
		var err error
		if data == nil {
			data, err = e.routeLoader(newCfg.RouteID)
		}
		if err != nil {
			return fmt.Errorf("%w: load route %q: %v", ErrInvalidRoute, newCfg.RouteID, err)
		}
		if _, err := LoadRouteCSV(data, LoadRouteCSVOptions{
			ResolvedRunStartMs: 1,
			MaxBytes:           newCfg.MaxRouteBytes,
			MaxRows:            newCfg.MaxRouteRows,
		}); err != nil {
			return fmt.Errorf("%w: validate route %q: %v", ErrInvalidRoute, newCfg.RouteID, err)
		}
		newRouteData = data
	}

	var newSender packetSender
	senderFieldsChanged := update.AggregatorURL != nil || update.RequestTimeoutMs != nil
	if senderFieldsChanged {
		telemetryURL, err := ValidateAggregatorURL(newCfg.AggregatorURL)
		if err != nil {
			return fmt.Errorf("simulator: config update: %w", err)
		}
		newSender = NewSender(telemetryURL, time.Duration(newCfg.RequestTimeoutMs)*time.Millisecond)
	}

	// Atomically apply, re-checking the revision still matches (SIM-06:
	// "atomically apply only if state/config revision still matches").
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.controlRevision != currentRevision {
		return ErrControlRevisionConflict
	}
	// Re-verify preconditions that could have changed between snapshot and
	// apply (defensive; controlMu already serializes control operations,
	// so in practice this cannot fail if it didn't fail above, but the
	// engine's own background Tick loop can change hasPending/unsent
	// state concurrently with us holding only controlMu, not mu, during
	// the off-lock phase above).
	if noPendingFields && e.pending != nil {
		return ErrFieldRequiresNoPending
	}
	if readyEmptyFields && (e.playbackState != PlaybackReady || e.totalUnsentLocked() != 0) {
		return ErrFieldRequiresReadyEmpty
	}
	if update.NormalReportIntervalSimulatedMs != nil && e.playbackState != PlaybackStopped && e.playbackState != PlaybackReady {
		return ErrFieldRequiresStopped
	}

	if update.NormalReportIntervalSimulatedMs != nil {
		// Seal the current partial window once under the OLD interval, then
		// anchor the new schedule at current virtual time, per
		// SIMULATOR_CHANGE_REQUEST.md's "interval changes require stopped
		// playback, seal the current partial window once, and anchor the new
		// schedule at current virtual time." Playback is STOPPED (clock
		// frozen) or READY (clock not yet started, elapsed 0 and nothing
		// accumulated) here, so this read is stable either way.
		e.forceCloseOpenWindowLocked()
		e.nextWindowCloseMs = e.vclock.VirtualElapsedMs() + *update.NormalReportIntervalSimulatedMs
	}

	e.cfg = newCfg
	e.fairness.SetWeights(newCfg.LiveWeight, newCfg.BacklogWeight)
	e.vclock.SetMultiplier(newCfg.ClockMultiplier)
	if newRouteData != nil {
		e.routeData = newRouteData
		e.routeID = newCfg.RouteID
		e.route = nil // not resolved until next Start
	}
	if newSender != nil {
		e.sender = newSender
	}
	e.controlRevision++

	return nil
}
