// This file implements the simulator's core state owner (SIM-02/03/04/05):
// playback/transmission lifecycle, FIFO queues, route generation, lane
// eligibility and rate limiting. A single *Engine instance owns all mutable
// run state, guarded by one mutex (mu); a separate controlMu serializes
// whole control-plane operations (lifecycle.go) so their off-lock I/O
// phases (route parsing, building a new Sender) cannot interleave with each
// other, while never blocking GET /status or the background Tick loop's
// brief critical sections. Network I/O (HTTP sends) and file I/O (route
// loading) are always performed without holding mu, per SIM-06's "do not
// hold the state owner while doing network or file I/O."
package simulator

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PlaybackState is the simulator's playback lifecycle state (SIM-02).
type PlaybackState string

const (
	PlaybackReady     PlaybackState = "READY"
	PlaybackRunning   PlaybackState = "RUNNING"
	PlaybackStopped   PlaybackState = "STOPPED"
	PlaybackDraining  PlaybackState = "DRAINING"
	PlaybackCompleted PlaybackState = "COMPLETED"
)

// TransmissionState is derived from the engine's pending/coverage-pause
// state (SIM-02): ENABLED, PAUSED (coverage outage), or BLOCKED_ERROR. A
// coverage pause layered on top of a blocked error is reported via the
// separate CoveragePaused status field, not a fourth TransmissionState
// value (SIM-06 GET /status: "status transmission_state remains
// BLOCKED_ERROR with coverage_paused=true").
type TransmissionState string

const (
	TransmissionEnabled      TransmissionState = "ENABLED"
	TransmissionPaused       TransmissionState = "PAUSED"
	TransmissionBlockedError TransmissionState = "BLOCKED_ERROR"
)

// packetSender abstracts Sender for testability.
type packetSender interface {
	Send(ctx context.Context, pkt *Packet) SendOutcome
}

// sequenceAllocator abstracts IdentityStore for testability.
type sequenceAllocator interface {
	ReserveNextSequence(simulatorID string, utcDate string) (int64, error)
}

// queuedRecord is one route record sitting in a lane queue, carrying the
// monotonic instant it was generated so partial-flush eligibility is
// tracked correctly even across lane moves: pausing coverage moves unsent
// live records into backlog preserving each record's original enqueue
// time, since "Flush age runs in wall time even while stopped/paused, so
// partial packets may be immediately eligible on resume" (SIM-04). We use
// the injected MonotonicClock (not calendar wall time) for this ageing
// comparison so tests can control it deterministically.
type queuedRecord struct {
	routeIndex int
	enqueuedAt time.Time
}

// pendingPacketState is the zero-or-one immutable pending packet/batch:
// either in flight, blocked after a failed send, or blocked after a failed
// packet-ID allocation (SIM-03/04). Once records are selected for an
// attempt they move out of the lane queues into this state and are never
// silently lost; they only leave it via a successful acknowledgment (->
// acknowledged) or an explicit reset discard (-> discarded).
type pendingPacketState struct {
	recordIndices []int // route indices this batch drew from, ascending
	lane          Lane

	// packet is nil until a packet ID has been successfully allocated and
	// the packet serialized. It is nil while identityFailure is true, and
	// is retained (unchanged) across a send-failure retry so the retry
	// resends the byte-identical packet.
	packet *Packet

	inFlight        bool
	failed          bool
	identityFailure bool // true if blocked by a failed packet-ID allocation, not a send
	retryAuthorized bool // one-shot, set by POST /resume-transmission {retry_failed:true}

	lastOutcome   *SendOutcome // nil when identityFailure
	attemptedAtMs int64
}

// LastErrorInfo mirrors the status last_error diagnostic (SIM-06/09).
type LastErrorInfo struct {
	Kind                string `json:"kind"`
	Message             string `json:"message"`
	HTTPStatus          int    `json:"http_status,omitempty"`
	AcceptanceUncertain bool   `json:"acceptance_uncertain"`
	PacketID            string `json:"packet_id,omitempty"`
	TimestampMs         int64  `json:"timestamp_ms"`
}

// Last-error kind constants.
const (
	ErrorKindNetwork          = "network_error"
	ErrorKindTimeout          = "timeout"
	ErrorKindNon200           = "non_200_status"
	ErrorKindMalformedReceipt = "malformed_receipt"
	ErrorKindIdentityAlloc    = "identity_allocation_failed"
)

// rateSample is one instantaneous count-at-time observation, used to
// compute rolling 5-second generated/acknowledged fix rates (SIM-05).
type rateSample struct {
	atMono time.Time
	count  int64
}

const rollingWindow = 5 * time.Second

// Engine owns all mutable simulator run state (SIM-02..06). Exactly one
// Engine instance exists per process; all access goes through its exported
// methods, which handle locking internally.
type Engine struct {
	mu        sync.Mutex
	controlMu sync.Mutex

	cfg         Config
	mono        MonotonicClock
	nowWall     func() time.Time
	identity    sequenceAllocator
	sender      packetSender
	routeLoader RouteLoader

	// routeData holds the raw validated bytes of the currently configured
	// route, so it can be re-resolved against a new run start without
	// re-reading the file from disk. routeID names which configured route
	// this is.
	routeData []byte
	routeID   string

	runID           string
	controlRevision int64

	playbackState  PlaybackState
	coveragePaused bool

	vclock *VirtualClock

	route              []RouteRecord
	resolvedRunStartMs int64
	runStartResolved   bool
	generatedCursor    int

	// liveQueue/backlogQueue hold fixes whose normal report window has
	// already closed: they are ready to be bundled into packets and sent,
	// per SIMULATOR_CHANGE_REQUEST.md. liveWindowAccum/backlogWindowAccum
	// hold fixes generated in the currently open window, not yet ready to
	// send; they move into the corresponding ready queue only when the
	// window closes (advanceReportWindowsLocked), never merely because
	// transmission capacity is available.
	liveQueue    []queuedRecord
	backlogQueue []queuedRecord

	liveWindowAccum    []queuedRecord
	backlogWindowAccum []queuedRecord
	// nextWindowCloseMs is the virtual-elapsed ms at which the currently
	// open report window closes (becomes ready to send). It advances by
	// cfg.NormalReportIntervalSimulatedMs each time a window closes, and is
	// re-anchored at the current virtual time when the interval changes at
	// runtime (always while stopped; see PutConfig).
	nextWindowCloseMs int64

	pending *pendingPacketState

	fairness *FairnessSelector
	// freshFirstPending is a one-shot, non-blocking preference (not a gate):
	// armed on ResumeTransmission when backlog is pending, it causes the
	// next ready live records to be force-flushed as an immediate (possibly
	// partial) live report ahead of the normal window boundary, so recovery
	// surfaces the freshest position as soon as possible. It never blocks
	// backlog sends (SIMULATOR_CHANGE_REQUEST.md: "Remove any gate that
	// blocks backlog waiting for a future live fix").
	freshFirstPending bool

	lastAttemptMonoTime time.Time
	haveLastAttempt     bool

	generatedFixes                int64
	acknowledgedFixesCount        int64
	discardedFixesCount           int64
	quarantinedFixesCount         int64
	packetsAttempted              int64
	packetsAcknowledged           int64
	latestGeneratedTimestampMs    int64
	latestAcknowledgedTimestampMs int64

	generatedSamples    []rateSample
	acknowledgedSamples []rateSample
	// attemptedSamples tracks packetsAttempted over the rolling window, used
	// to report observed_packets_per_second (actual outbound attempt rate)
	// alongside the configured target rate (SIMULATOR_CHANGE_REQUEST.md).
	attemptedSamples []rateSample

	lastError *LastErrorInfo

	// testLastSelectedLane/testLastSelectedLaneSet/testSelectionCount are
	// test-only observation hooks (see engine_export_test.go), recording
	// the lane chosen by the most recent ordinary (non-retry) selection and
	// a monotonic count of such selections (incremented synchronously
	// within Tick, unlike sentCount on a test fake sender which is updated
	// from a separate dispatch goroutine and so cannot be read reliably
	// immediately after Tick returns). These add negligible footprint and
	// are never read outside tests.
	testLastSelectedLane    Lane
	testLastSelectedLaneSet bool
	testSelectionCount      int

	stopCh chan struct{}
	wg     sync.WaitGroup
	closed bool
}

// RouteLoader reads the raw bytes of a named route (keyed by route_id, as
// configured in Config.Routes). In production this reads the configured
// file path from disk; tests can supply an in-memory implementation.
type RouteLoader func(routeID string) ([]byte, error)

// EngineOptions configures a new Engine.
type EngineOptions struct {
	Config      Config
	Mono        MonotonicClock
	Identity    sequenceAllocator
	Sender      packetSender     // if nil, a real Sender is constructed from Config
	NowWall     func() time.Time // if nil, time.Now is used
	RouteData   []byte           // raw bytes of the initially configured route (already read from disk)
	RouteLoader RouteLoader      // used by PutConfig to load a newly selected route_id; required if route_id may ever change at runtime
}

// NewEngine constructs an Engine in the READY state from an already-loaded
// route. Call Run to start its background ticker goroutine, and Close to
// stop it.
func NewEngine(opts EngineOptions) (*Engine, error) {
	mono := opts.Mono
	if mono == nil {
		mono = SystemMonotonicClock{}
	}
	nowWall := opts.NowWall
	if nowWall == nil {
		nowWall = time.Now
	}
	sender := opts.Sender
	if sender == nil {
		telemetryURL, err := ValidateAggregatorURL(opts.Config.AggregatorURL)
		if err != nil {
			return nil, fmt.Errorf("simulator: engine: %w", err)
		}
		sender = NewSender(telemetryURL, time.Duration(opts.Config.RequestTimeoutMs)*time.Millisecond)
	}

	// Validate the initial route structurally using a placeholder resolved
	// run start; the real resolved start (possibly "now", decided at Start
	// time) is substituted when a run actually begins. The placeholder
	// parse result is still stored as e.route so GET /status can report
	// route_rows even before the first Start; its absolute timestamps are
	// meaningless until Start recomputes them, but generateDueLocked only
	// ever runs while RUNNING, which is reachable only via Start, which
	// always overwrites e.route with correctly resolved timestamps first.
	route, err := LoadRouteCSV(opts.RouteData, LoadRouteCSVOptions{
		ResolvedRunStartMs: 1,
		MaxBytes:           opts.Config.MaxRouteBytes,
		MaxRows:            opts.Config.MaxRouteRows,
	})
	if err != nil {
		return nil, fmt.Errorf("simulator: engine: initial route %q invalid: %w", opts.Config.RouteID, err)
	}

	e := &Engine{
		cfg:           opts.Config,
		mono:          mono,
		nowWall:       nowWall,
		identity:      opts.Identity,
		sender:        sender,
		routeLoader:   opts.RouteLoader,
		routeData:     opts.RouteData,
		routeID:       opts.Config.RouteID,
		playbackState: PlaybackReady,
		route:         route,
		vclock:        NewVirtualClock(mono, opts.Config.ClockMultiplier),
		fairness:      NewFairnessSelector(opts.Config.LiveWeight, opts.Config.BacklogWeight),
		stopCh:        make(chan struct{}),
	}
	return e, nil
}

// tickInterval is the background loop's fixed wall-clock wake-up period.
// A short fixed poll interval keeps the implementation simple (vs. a fully
// event-scheduled timer) while comfortably supporting this project's
// demo-scale rates, and naturally yields the lock between generation
// chunks and send attempts so control requests stay responsive.
const tickInterval = 5 * time.Millisecond

// maxGenerationChunk bounds how many route records are generated per Tick,
// per SIM-02's chunking guidance ("chunk work... e.g. <=1000 records per
// chunk"); the regular tick cadence is what yields the lock to control
// requests between chunks.
const maxGenerationChunk = 1000

// Run starts the engine's background ticker goroutine. It returns
// immediately; call Close to stop it. Run must be called at most once.
func (e *Engine) Run() {
	e.wg.Add(1)
	go e.tickLoop()
}

// Close stops the background ticker goroutine and waits for it to exit.
// Close must be called at most once.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	e.mu.Unlock()
	close(e.stopCh)
	e.wg.Wait()
}

func (e *Engine) tickLoop() {
	defer e.wg.Done()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.Tick()
		}
	}
}

// Tick performs one wall-clock wake-up's worth of work: generate any due
// route records (one bounded chunk), then, if eligible and rate limits
// allow, launch at most one packet send in its own goroutine. Exported so
// tests can drive the engine deterministically without the background
// goroutine/real timers. Tick itself never blocks on network I/O: the send
// (identity allocation + HTTP POST) always runs in a separate goroutine so
// a slow/stuck aggregator can never stall route generation or control-API
// responsiveness (SIM-06/09). Callers that need to observe a dispatched
// send's result should poll Status() (e.g. via a helper like this
// package's test-only waitForPendingCleared) rather than assuming Tick's
// return means the send has completed.
func (e *Engine) Tick() {
	e.mu.Lock()
	e.generateDueLocked()
	e.checkCompletionLocked()
	attempt := e.prepareSendAttemptLocked()
	e.mu.Unlock()

	if attempt != nil {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			e.dispatchSend(attempt)
		}()
	}
}

// generateDueLocked generates up to maxGenerationChunk route records whose
// event timestamp is now due given the virtual clock's current elapsed
// time, accumulating each into the currently open report window's live or
// backlog accumulator (depending on whether coverage is currently paused),
// closing any report window(s) whose simulated-time boundary a record's
// event time has reached or passed, and tracking generation counters/
// watermarks. Per SIMULATOR_CHANGE_REQUEST.md, report windows are a pure
// function of simulated (event) time: a window closes -- and its
// accumulated fixes become eligible to send -- strictly because virtual
// time has advanced past its boundary, never because transmission capacity
// happens to be available. Caller must hold e.mu.
func (e *Engine) generateDueLocked() {
	if e.playbackState != PlaybackRunning {
		return
	}

	virtualNowMs := e.vclock.VirtualElapsedMs()
	deadlineAbsMs := e.resolvedRunStartMs + virtualNowMs
	nowMono := e.mono.Now()

	end := e.generatedCursor
	for end < len(e.route) && (end-e.generatedCursor) < maxGenerationChunk && e.route[end].TimestampMs <= deadlineAbsMs {
		end++
	}
	if end == e.generatedCursor {
		// No new records this tick, but simulated time may still have
		// crossed a window boundary with nothing to generate in it (a gap
		// in source timing, or simply waiting between sparse rows).
		e.closeDueReportWindowsLocked(virtualNowMs)
		return
	}

	for i := e.generatedCursor; i < end; i++ {
		elapsedMs := e.route[i].TimestampMs - e.resolvedRunStartMs
		// Close any window(s) this record's event time has reached/passed
		// BEFORE accumulating it, so it lands in the correct (possibly new)
		// current window, and so a record landing exactly on a boundary is
		// correctly placed in the window that STARTS there (half-open
		// [start,end)), not the one that just closed.
		e.closeDueReportWindowsLocked(elapsedMs)

		rec := queuedRecord{routeIndex: i, enqueuedAt: nowMono}
		if e.coveragePaused {
			e.backlogWindowAccum = append(e.backlogWindowAccum, rec)
		} else {
			e.liveWindowAccum = append(e.liveWindowAccum, rec)
		}
		e.generatedFixes++
		if e.route[i].TimestampMs > e.latestGeneratedTimestampMs {
			e.latestGeneratedTimestampMs = e.route[i].TimestampMs
		}
	}
	e.recordGeneratedSampleLocked(nowMono)
	e.generatedCursor = end

	if e.generatedCursor >= len(e.route) {
		e.playbackState = PlaybackDraining
		// EOF flushes remaining fixes: seal whatever window is still open
		// immediately, regardless of whether its boundary has been reached,
		// so the last partial window is not stranded forever (SIM-02/04,
		// SIMULATOR_CHANGE_REQUEST.md: "EOF flushes remaining fixes and
		// continues draining").
		e.forceCloseOpenWindowLocked()
		return
	}

	// Close any window(s) whose boundary virtual time has now passed even
	// though the last generated record didn't land exactly on/past it
	// (e.g. the chunk ended mid-window because maxGenerationChunk was hit,
	// or the next due record's timestamp is still in the future).
	e.closeDueReportWindowsLocked(virtualNowMs)
}

// closeDueReportWindowsLocked closes every report window whose boundary is
// <= elapsedMs (there may be more than one, e.g. after a large catch-up
// chunk or a sparse CSV gap spanning multiple windows), moving each
// window's accumulated live/backlog fixes into the corresponding ready
// queue. A window with nothing accumulated produces no report ("no empty
// reports"). Caller must hold e.mu.
func (e *Engine) closeDueReportWindowsLocked(elapsedMs int64) {
	for elapsedMs >= e.nextWindowCloseMs {
		e.forceCloseOpenWindowLocked()
		e.nextWindowCloseMs += e.cfg.NormalReportIntervalSimulatedMs
	}
}

// forceCloseOpenWindowLocked moves whatever is currently accumulated for
// the open report window (live and/or backlog) into the corresponding
// ready-to-send queue, without touching nextWindowCloseMs. Used both by
// ordinary boundary-driven closes (closeDueReportWindowsLocked, which
// advances nextWindowCloseMs itself afterward) and by EOF/runtime-interval-
// change sealing, which reposition nextWindowCloseMs separately. Caller
// must hold e.mu.
func (e *Engine) forceCloseOpenWindowLocked() {
	if len(e.liveWindowAccum) > 0 {
		e.liveQueue = append(e.liveQueue, e.liveWindowAccum...)
		e.liveWindowAccum = nil
	}
	if len(e.backlogWindowAccum) > 0 {
		e.backlogQueue = append(e.backlogQueue, e.backlogWindowAccum...)
		e.backlogWindowAccum = nil
	}
}

// checkCompletionLocked transitions DRAINING -> COMPLETED once no
// queued/pending work remains. Caller must hold e.mu.
func (e *Engine) checkCompletionLocked() {
	if e.playbackState != PlaybackDraining {
		return
	}
	if e.totalUnsentLocked() == 0 {
		e.playbackState = PlaybackCompleted
	}
}

// totalUnsentLocked is live+backlog+pending record counts, including fixes
// still accumulating in an not-yet-closed report window (they are
// generated and must never be reported lost, even though they are not yet
// eligible to send). Caller must hold e.mu.
func (e *Engine) totalUnsentLocked() int {
	n := len(e.liveQueue) + len(e.backlogQueue) + len(e.liveWindowAccum) + len(e.backlogWindowAccum)
	if e.pending != nil {
		n += len(e.pending.recordIndices)
	}
	return n
}

// isLaneEligibleLocked reports whether queue currently qualifies for an
// ordinary send attempt, per SIMULATOR_CHANGE_REQUEST.md: it is eligible
// whenever it holds any fixes at all, since by construction a record only
// ever reaches liveQueue/backlogQueue once its report window has already
// closed (closeDueReportWindowsLocked/forceCloseOpenWindowLocked) -- there
// is no additional size or wall-clock-age gate here. packet_size still
// bounds how many records a single attempt draws from the queue (a safety
// bound on wire size, applied in prepareSendAttemptLocked), not whether the
// queue is eligible at all. Caller must hold e.mu.
func (e *Engine) isLaneEligibleLocked(queue []queuedRecord) bool {
	return len(queue) > 0
}

// rateIntervalLocked returns the minimum wall-clock spacing between send
// attempts (SIM-05), scaled by the clock multiplier: configured capacity is
// a simulated-time rate (max_packets_per_simulated_second), so the actual
// enforced wall-clock rate is that rate multiplied by ClockMultiplier (per
// SIMULATOR_CHANGE_REQUEST.md: "actual packet-rate ceiling = configured
// rate x M"). Caller must hold e.mu.
func (e *Engine) rateIntervalLocked() time.Duration {
	actualRate := e.cfg.MaxPacketsPerSimulatedSecond * e.vclock.Multiplier()
	return time.Duration(float64(time.Second) / actualRate)
}

// recordGeneratedSampleLocked and recordAcknowledgedSampleLocked append a
// rolling-window sample and prune samples older than rollingWindow. Caller
// must hold e.mu.
func (e *Engine) recordGeneratedSampleLocked(now time.Time) {
	e.generatedSamples = pruneSamples(append(e.generatedSamples, rateSample{atMono: now, count: e.generatedFixes}), now)
}

func (e *Engine) recordAcknowledgedSampleLocked(now time.Time) {
	e.acknowledgedSamples = pruneSamples(append(e.acknowledgedSamples, rateSample{atMono: now, count: e.acknowledgedFixesCount}), now)
}

func pruneSamples(samples []rateSample, now time.Time) []rateSample {
	cutoff := now.Add(-rollingWindow)
	i := 0
	for i < len(samples) && samples[i].atMono.Before(cutoff) {
		i++
	}
	return samples[i:]
}

// rollingRate computes the observed rate (count/second) over the rolling
// window from the oldest retained sample to now.
func rollingRate(samples []rateSample, currentCount int64, now time.Time) float64 {
	if len(samples) == 0 {
		return 0
	}
	oldest := samples[0]
	elapsed := now.Sub(oldest.atMono).Seconds()
	if elapsed <= 0 {
		return 0
	}
	delta := currentCount - oldest.count
	return float64(delta) / elapsed
}
