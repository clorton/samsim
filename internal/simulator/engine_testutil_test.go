package simulator

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// fakeSender is a packetSender test double that queues outcomes to return,
// defaulting to an always-succeeding receipt matching the packet, and
// records every packet it was asked to send.
type fakeSender struct {
	mu          sync.Mutex
	sent        []*Packet
	nextOutcome func(pkt *Packet) SendOutcome
	blockUntil  chan struct{} // if non-nil, Send blocks until this is closed
}

func newFakeSender() *fakeSender {
	return &fakeSender{}
}

func (f *fakeSender) Send(ctx context.Context, pkt *Packet) SendOutcome {
	f.mu.Lock()
	f.sent = append(f.sent, pkt)
	fn := f.nextOutcome
	block := f.blockUntil
	f.mu.Unlock()

	if block != nil {
		<-block
	}

	if fn != nil {
		return fn(pkt)
	}
	return SendOutcome{Receipt: &AcceptanceReceipt{
		SimulatorID:     pkt.SimulatorID,
		PacketID:        pkt.PacketID,
		DuplicatePacket: false,
		AcceptedFixes:   pkt.FixCount,
	}}
}

func (f *fakeSender) setOutcome(fn func(pkt *Packet) SendOutcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextOutcome = fn
}

func (f *fakeSender) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeSender) lastSent() *Packet {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return nil
	}
	return f.sent[len(f.sent)-1]
}

// fakeIdentity is a sequenceAllocator test double backed by an in-memory
// counter, optionally forced to fail.
type fakeIdentity struct {
	mu       sync.Mutex
	counters map[string]int64
	failNext atomic.Bool
}

func newFakeIdentity() *fakeIdentity {
	return &fakeIdentity{counters: map[string]int64{}}
}

func (f *fakeIdentity) ReserveNextSequence(simulatorID, utcDate string) (int64, error) {
	if f.failNext.CompareAndSwap(true, false) {
		return 0, fmt.Errorf("fake identity allocation failure")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := simulatorID + "|" + utcDate
	f.counters[key]++
	return f.counters[key], nil
}

func (f *fakeIdentity) forceNextFailure() {
	f.failNext.Store(true)
}

// testEngineDeps bundles everything needed to construct a test Engine with
// full control over time and I/O.
type testEngineDeps struct {
	mono     *FakeMonotonicClock
	sender   *fakeSender
	identity *fakeIdentity
	nowWall  func() time.Time
}

func newTestEngine(t testingT, cfgMutate func(*Config), routeCSV string) (*Engine, *testEngineDeps) {
	t.Helper()

	cfg := Default()
	cfg.SimulatorID = "sim-test"
	cfg.VehicleID = "vehicle-test"
	cfg.Routes = map[string]string{"demo": "unused-in-tests"}
	cfg.RouteID = "demo"
	cfg.RunStartMs = RunStart{FixedMs: 1_700_000_000_000}
	cfg.PacketSize = 10
	cfg.PartialFlushWallMs = 200
	cfg.MaxPacketsPerSecond = 1000 // fast by default; individual tests override for rate testing
	cfg.LiveWeight = 3
	cfg.BacklogWeight = 1
	if cfgMutate != nil {
		cfgMutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid test config: %v", err)
	}

	mono := NewFakeMonotonicClock(time.Unix(1_600_000_000, 0))
	sender := newFakeSender()
	identity := newFakeIdentity()
	nowWall := func() time.Time { return time.UnixMilli(cfg.RunStartMs.FixedMs) }

	e, err := NewEngine(EngineOptions{
		Config:    cfg,
		Mono:      mono,
		Identity:  identity,
		Sender:    sender,
		NowWall:   nowWall,
		RouteData: []byte(routeCSV),
		RouteLoader: func(routeID string) ([]byte, error) {
			return []byte(routeCSV), nil
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	return e, &testEngineDeps{mono: mono, sender: sender, identity: identity, nowWall: nowWall}
}

// testingT is the minimal subset of *testing.T used by test helpers in this
// file, so they can be called from table-driven subtests etc. without extra
// imports in every call site.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// waitForSentCount polls (via repeated Tick calls, never real sleeps) until
// the fake sender has recorded at least n sends, or fails the test after a
// bounded number of iterations. The caller is responsible for advancing the
// fake clock as needed between calls if time-based eligibility must be
// satisfied; this helper exists mainly to let the async dispatchSend
// goroutine finish and commit its result before the next assertion.
func waitForSentCount(t testingT, sender *fakeSender, n int) {
	t.Helper()
	for i := 0; i < 20000; i++ {
		if sender.sentCount() >= n {
			return
		}
		time.Sleep(100 * time.Microsecond)
	}
	t.Fatalf("timed out waiting for sentCount >= %d, got %d", n, sender.sentCount())
}

// waitForPendingCleared polls until the engine has no in-flight pending
// attempt (either fully resolved or blocked, but not mid-flight), so tests
// can safely inspect state after Tick triggers an async send.
func waitForPendingCleared(t testingT, e *Engine) {
	t.Helper()
	for i := 0; i < 20000; i++ {
		e.mu.Lock()
		inFlight := e.pending != nil && e.pending.inFlight
		e.mu.Unlock()
		if !inFlight {
			return
		}
		time.Sleep(100 * time.Microsecond)
	}
	t.Fatalf("timed out waiting for in-flight send to clear")
}
