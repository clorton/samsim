package simulator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gpsdemo/internal/aggregator"
	"gpsdemo/internal/clock"
	"gpsdemo/internal/events"
	"gpsdemo/internal/httpapi"
	"gpsdemo/internal/idgen"
	"gpsdemo/internal/storage"
)

// newTestAggregator spins up a real aggregator (storage + service + HTTP)
// against a temp SQLite file, for true end-to-end simulator<->aggregator
// integration tests (SIM-07's S17/S18, independent of the aggregator's own
// test suite).
func newTestAggregatorServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(context.TODO(), storage.Options{
		Path:                  dir + "/test.sqlite",
		BusyTimeoutMs:         5000,
		TripGapSeconds:        600,
		ExpectedFixIntervalMs: 1000,
	})
	if err != nil {
		t.Fatalf("open aggregator store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	hub := events.NewHub(64)
	clk := clock.System{}
	ids := idgen.UUID{}
	svc := aggregator.NewService(store, hub, clk, ids, 10000)

	server := &httpapi.Server{
		Service:           svc,
		Hub:               hub,
		MaxBodyBytes:      1 << 20,
		MaxFixesPerPacket: 10000,
		SnapshotMaxBytes:  16 << 20,
	}
	ts := httptest.NewServer(server.Router())
	t.Cleanup(ts.Close)
	return ts
}

func aggregatorTrips(t *testing.T, baseURL, vehicleID string) []map[string]any {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/api/v1/vehicles/%s/trips?include_merged=true", baseURL, vehicleID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal trips response %s: %v", body, err)
	}
	return env.Items
}

func currentTrips(items []map[string]any) []map[string]any {
	var out []map[string]any
	for _, it := range items {
		if it["close_reason"] != "MERGED" {
			out = append(out, it)
		}
	}
	return out
}

// buildLongRouteCSV generates a route at 1-second cadence spanning
// durationSeconds, with coordinates that stay in valid range.
func buildLongRouteCSV(durationSeconds int) string {
	var b strings.Builder
	b.WriteString("ts,lat,lon,speed_mps,heading_deg\n")
	for i := 0; i <= durationSeconds; i++ {
		lon := -120.0 + float64(i%1000)*0.0001
		fmt.Fprintf(&b, "%d.0,40.0,%v,1.0,0.0\n", i, lon)
	}
	return b.String()
}

// TestS18_OutageProducesTwoTripsThenMerge implements SIM-07's S18 / E2E-01:
// against a real aggregator, an outage longer than 600 simulated seconds
// produces two current trips, and resuming with sufficient backlog-drain
// capacity produces a new merged trip ID with MERGED predecessors and
// correct lineage, with canonical position never regressing.
//
// The test gates the underlying HTTP sender so it can deterministically
// observe the intermediate "two trips" state after the first post-outage
// live send, before permitting backlog drain to proceed -- exactly as
// SIMULATOR_SPEC.md's S18 describes ("Gate sender in the test after first
// recovery live receipt and assert two current trips before permitting
// backlog drain").
func TestS18_OutageProducesTwoTripsThenMerge(t *testing.T) {
	aggURL := newTestAggregatorServer(t)
	telemetryURL, err := ValidateAggregatorURL(aggURL.URL)
	if err != nil {
		t.Fatal(err)
	}

	const vehicleID = "vehicle-s18"
	const routeSeconds = 100000 // long enough that the route is never exhausted by this test's phases

	cfg := Default()
	cfg.SimulatorID = "sim-s18"
	cfg.VehicleID = vehicleID
	cfg.AggregatorURL = aggURL.URL
	cfg.Routes = map[string]string{"demo": "unused"}
	cfg.RouteID = "demo"
	cfg.RunStartMs = RunStart{FixedMs: 1_700_000_000_000}
	cfg.ClockMultiplier = 1000
	cfg.PacketSize = 10
	// 10 simulated seconds per report, matching ExpectedFixIntervalMs's
	// default 1Hz source cadence, normally yields 10 fixes/report (matching
	// PacketSize); at 1000x multiplier that is a 10ms actual interval.
	cfg.NormalReportIntervalSimulatedMs = 10000
	// 10 packets/simulated-second * 1000x multiplier = 10000/s actual,
	// comfortably enough capacity to drain backlog within this test.
	cfg.MaxPacketsPerSimulatedSecond = 10
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	mono := NewFakeMonotonicClock(time.Unix(1_600_000_000, 0))
	identity := newFakeIdentity()
	nowWall := func() time.Time { return time.UnixMilli(cfg.RunStartMs.FixedMs) }

	// Gatable real sender wrapping the actual HTTP Sender, so the test can
	// pause delivery deterministically at the moment described by the
	// spec (after the first post-outage live receipt).
	realSender := NewSender(telemetryURL, time.Duration(cfg.RequestTimeoutMs)*time.Millisecond)
	gate := newSendGate(realSender)

	e, err := NewEngine(EngineOptions{
		Config:    cfg,
		Mono:      mono,
		Identity:  identity,
		Sender:    gate,
		NowWall:   nowWall,
		RouteData: []byte(buildLongRouteCSV(routeSeconds)),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	tick := func() { e.Tick() }
	// advanceAndDrainSends advances the fake monotonic clock by d up front
	// (to generate due route records), then ticks maxTicks times, nudging
	// the clock forward slightly on each iteration so the rate limiter
	// (which reads this same fake clock) doesn't freeze solid after its
	// first attempt sets its anchor.
	advanceAndDrainSends := func(d time.Duration, maxTicks int) {
		mono.Advance(d)
		for i := 0; i < maxTicks; i++ {
			tick()
			mono.Advance(200 * time.Microsecond)
			time.Sleep(50 * time.Microsecond)
		}
	}

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}

	// Establish an initial trip: generate and send some data normally.
	advanceAndDrainSends(200*time.Millisecond, 2000) // *1000 -> 200 virtual seconds of live data, sent in ~20 packets
	if e.Status().AcknowledgedFixes == 0 {
		t.Fatal("expected initial acknowledgment before outage")
	}
	firstAckCount := e.Status().AcknowledgedFixes

	// Pause coverage (outage begins) and advance virtual time well past
	// 600 simulated seconds while paused, building backlog.
	e.PauseTransmission()
	advanceAndDrainSends(700*time.Millisecond, 10) // *1000 -> 700 more virtual seconds, all into backlog (no sends while paused); outage duration alone exceeds the 600s threshold
	if e.Status().BacklogQueueCount == 0 {
		t.Fatal("expected backlog to have accumulated during outage")
	}
	backlogDuringOutage := e.Status().BacklogQueueCount

	// Resume: gate the sender so only the first recovery live send is
	// allowed through; backlog sends are blocked until the test lifts the
	// gate.
	gate.blockAfter(1)
	e.ResumeTransmission(ResumeTransmissionOptions{})
	// Trickle in a small amount of new virtual time (enough for a few new
	// live fixes to clear the gate), then keep nudging the fake monotonic
	// clock forward a little on each tick so the rate limiter's anchor
	// (which uses this same fake clock) doesn't freeze all further
	// attempts solid once the first one sets it.
	mono.Advance(5 * time.Millisecond)
	for i := 0; i < 2000; i++ {
		tick()
		mono.Advance(200 * time.Microsecond)
		time.Sleep(50 * time.Microsecond)
	}

	gate.waitForBlocked(t, 5*time.Second)

	// At this point exactly one post-outage live packet has been
	// delivered and acknowledged; the aggregator must now show two
	// current trips (the original pre-outage trip, now INACTIVE, and a
	// new post-outage trip, ACTIVE), not yet merged.
	trips := currentTrips(aggregatorTrips(t, aggURL.URL, vehicleID))
	if len(trips) != 2 {
		t.Fatalf("expected 2 current trips after first post-outage live send, got %d: %+v", len(trips), trips)
	}

	var preOutagePosition float64
	for _, tr := range trips {
		endMs := tr["end_ms"].(float64)
		if endMs > preOutagePosition {
			preOutagePosition = endMs
		}
	}

	// Now allow backlog drain to proceed. Advance the fake monotonic clock
	// by the smallest possible increment each tick -- just enough to clear
	// each attempt's rate-limit spacing requirement -- rather than a larger
	// step. At this config's 1000x multiplier even 1 microsecond of
	// monotonic advance per tick accumulates virtual time, so bound the
	// loop by unsent count rather than a fixed iteration budget alone, and
	// keep per-tick advances minimal to limit how much fresh live data
	// generates while draining the existing backlog.
	gate.unblock()
	for i := 0; i < 50000 && e.Status().TotalUnsentFixes > 0; i++ {
		tick()
		mono.Advance(1 * time.Microsecond)
		time.Sleep(50 * time.Microsecond)
	}

	// Any still-unsent fixes at this point are legitimately sitting in the
	// current open report window, which has not yet reached its simulated-
	// time boundary (SIMULATOR_CHANGE_REQUEST.md: windows close purely on
	// elapsed simulated time, never merely because capacity is available).
	// The route is intentionally far from exhausted, so EOF won't seal it;
	// force the trailing window closed the same way an operator would
	// (stop, shrink the interval, resume) so the test can assert a true
	// zero-unsent end state rather than leaving an artificial tail.
	if e.Status().TotalUnsentFixes > 0 {
		e.Stop()
		tinyInterval := int64(1)
		if err := e.PutConfig(ConfigUpdate{NormalReportIntervalSimulatedMs: &tinyInterval}); err != nil {
			t.Fatalf("seal trailing window: %v", err)
		}
		e.Start()
		for i := 0; i < 20000 && e.Status().TotalUnsentFixes > 0; i++ {
			tick()
			mono.Advance(1 * time.Microsecond)
			time.Sleep(50 * time.Microsecond)
		}
	}
	if e.Status().TotalUnsentFixes != 0 {
		t.Fatalf("expected all queues to fully drain, got %d unsent (live=%d backlog=%d live_pending=%d backlog_pending=%d)",
			e.Status().TotalUnsentFixes, e.Status().LiveQueueCount, e.Status().BacklogQueueCount, e.Status().LiveWindowPendingCount, e.Status().BacklogWindowPendingCount)
	}

	tripsAfterDrain := aggregatorTrips(t, aggURL.URL, vehicleID)
	current := currentTrips(tripsAfterDrain)
	if len(current) != 1 {
		t.Fatalf("expected exactly 1 current (merged) trip after backlog drain, got %d: %+v", len(current), current)
	}

	var mergedCount int
	for _, tr := range tripsAfterDrain {
		if tr["close_reason"] == "MERGED" {
			mergedCount++
			if tr["superseded_by_trip_id"] == nil {
				t.Fatalf("expected MERGED predecessor to have a successor: %+v", tr)
			}
		}
	}
	if mergedCount != 2 {
		t.Fatalf("expected exactly 2 MERGED predecessors (the pre-outage and first-post-outage trips), got %d: %+v", mergedCount, tripsAfterDrain)
	}

	finalPosition := current[0]["end_ms"].(float64)
	if finalPosition < preOutagePosition {
		t.Fatalf("canonical position regressed: was %v, now %v", preOutagePosition, finalPosition)
	}

	if e.Status().AcknowledgedFixes <= firstAckCount {
		t.Fatal("expected acknowledgment count to have grown after backlog drain")
	}
	_ = backlogDuringOutage
}

// sendGate wraps a real packetSender, letting a test allow a bounded
// number of sends through before blocking all further sends (without
// returning a failure outcome -- the attempt simply does not complete)
// until explicitly unblocked. This lets S18's test deterministically
// observe the aggregator's intermediate "two trips" state after exactly
// one post-outage live packet is delivered, before permitting backlog
// drain to proceed.
type sendGate struct {
	underlying packetSender

	mu          sync.Mutex
	remaining   int // sends still allowed through before blocking; -1 means unlimited
	blockedCh   chan struct{}
	blockedOnce sync.Once
	proceedCh   chan struct{}
}

func newSendGate(underlying packetSender) *sendGate {
	return &sendGate{underlying: underlying, remaining: -1, blockedCh: make(chan struct{}), proceedCh: make(chan struct{})}
}

// blockAfter allows the next n sends through, then blocks all subsequent
// sends until unblock is called.
func (g *sendGate) blockAfter(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.remaining = n
	g.blockedCh = make(chan struct{})
	g.proceedCh = make(chan struct{})
	g.blockedOnce = sync.Once{}
}

// unblock releases any currently-blocked send and allows all future sends
// through immediately.
func (g *sendGate) unblock() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.remaining = -1
	close(g.proceedCh)
}

// waitForBlocked waits until a send has actually become blocked by this
// gate, or fails the test after timeout.
func (g *sendGate) waitForBlocked(t testingT, timeout time.Duration) {
	t.Helper()
	g.mu.Lock()
	ch := g.blockedCh
	g.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for send gate to block")
	}
}

func (g *sendGate) Send(ctx context.Context, pkt *Packet) SendOutcome {
	g.mu.Lock()
	if g.remaining == 0 {
		proceedCh := g.proceedCh
		g.blockedOnce.Do(func() { close(g.blockedCh) })
		g.mu.Unlock()
		<-proceedCh
	} else {
		if g.remaining > 0 {
			g.remaining--
		}
		g.mu.Unlock()
	}
	return g.underlying.Send(ctx, pkt)
}
