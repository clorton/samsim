package simulator

import (
	"context"
	"testing"
	"time"
)

// TestS17_BothEncodingsWorkAgainstRealAggregator implements S17: both JSON
// and binary Protobuf encodings work against a real aggregator.
func TestS17_BothEncodingsWorkAgainstRealAggregator(t *testing.T) {
	for _, encoding := range []string{"json", "protobuf"} {
		t.Run(encoding, func(t *testing.T) {
			aggURL := newTestAggregatorServer(t)
			telemetryURL, err := ValidateAggregatorURL(aggURL.URL)
			if err != nil {
				t.Fatal(err)
			}

			vehicleID := "vehicle-s17-" + encoding
			cfg := Default()
			cfg.SimulatorID = "sim-s17-" + encoding
			cfg.VehicleID = vehicleID
			cfg.AggregatorURL = aggURL.URL
			cfg.Routes = map[string]string{"demo": "unused"}
			cfg.RouteID = "demo"
			cfg.RunStartMs = RunStart{FixedMs: 1_700_000_000_000}
			cfg.Encoding = encoding
			cfg.ClockMultiplier = 100
			cfg.PacketSize = 10
			cfg.PartialFlushWallMs = 50
			if err := cfg.Validate(); err != nil {
				t.Fatalf("invalid config: %v", err)
			}

			mono := NewFakeMonotonicClock(time.Unix(1_600_000_000, 0))
			identity := newFakeIdentity()
			nowWall := func() time.Time { return time.UnixMilli(cfg.RunStartMs.FixedMs) }
			sender := NewSender(telemetryURL, time.Duration(cfg.RequestTimeoutMs)*time.Millisecond)

			e, err := NewEngine(EngineOptions{
				Config: cfg, Mono: mono, Identity: identity, Sender: sender, NowWall: nowWall,
				RouteData: []byte(buildRouteCSV(100)),
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := e.Start(); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 1000 && e.Status().AcknowledgedFixes < 100; i++ {
				mono.Advance(2 * time.Millisecond)
				e.Tick()
				time.Sleep(200 * time.Microsecond)
			}

			st := e.Status()
			if st.AcknowledgedFixes != 100 {
				t.Fatalf("expected all 100 fixes acknowledged, got %d (last_error=%+v)", st.AcknowledgedFixes, st.LastError)
			}

			trips := currentTrips(aggregatorTrips(t, aggURL.URL, vehicleID))
			if len(trips) != 1 {
				t.Fatalf("expected 1 current trip on the aggregator, got %d", len(trips))
			}
			if int(trips[0]["fix_count"].(float64)) != 100 {
				t.Fatalf("expected trip fix_count 100, got %v", trips[0]["fix_count"])
			}
		})
	}
}

// TestS17_RetryAfterLostResponseChangesStateExactlyOnce implements part of
// S17: retry after a simulated lost response changes aggregator state
// exactly once (AG-04 idempotency exercised end-to-end from the simulator
// side via explicit resume-transmission retry).
func TestS17_RetryAfterLostResponseChangesStateExactlyOnce(t *testing.T) {
	aggURL := newTestAggregatorServer(t)
	telemetryURL, err := ValidateAggregatorURL(aggURL.URL)
	if err != nil {
		t.Fatal(err)
	}

	const vehicleID = "vehicle-s17-retry"
	cfg := Default()
	cfg.SimulatorID = "sim-s17-retry"
	cfg.VehicleID = vehicleID
	cfg.AggregatorURL = aggURL.URL
	cfg.Routes = map[string]string{"demo": "unused"}
	cfg.RouteID = "demo"
	cfg.RunStartMs = RunStart{FixedMs: 1_700_000_000_000}
	cfg.PacketSize = 5
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	mono := NewFakeMonotonicClock(time.Unix(1_600_000_000, 0))
	identity := newFakeIdentity()
	nowWall := func() time.Time { return time.UnixMilli(cfg.RunStartMs.FixedMs) }

	// Wrap the real sender so the first attempt's response is "lost" (we
	// still let the real HTTP request go through and commit on the
	// aggregator, but report a network-error-like failure to the engine,
	// simulating a client-side timeout/lost response after the server
	// already committed).
	realSender := NewSender(telemetryURL, time.Duration(cfg.RequestTimeoutMs)*time.Millisecond)
	lossy := &lossyOnceSender{underlying: realSender}

	e, err := NewEngine(EngineOptions{
		Config: cfg, Mono: mono, Identity: identity, Sender: lossy, NowWall: nowWall,
		RouteData: []byte(buildRouteCSV(5)),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	if e.Status().TransmissionState != TransmissionBlockedError {
		t.Fatalf("expected blocked after simulated lost response, got %s", e.Status().TransmissionState)
	}

	if err := e.ResumeTransmission(ResumeTransmissionOptions{RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e)

	if e.Status().TransmissionState != TransmissionEnabled {
		t.Fatalf("expected retry to succeed, got %s (last_error=%+v)", e.Status().TransmissionState, e.Status().LastError)
	}

	trips := currentTrips(aggregatorTrips(t, aggURL.URL, vehicleID))
	if len(trips) != 1 {
		t.Fatalf("expected exactly 1 current trip, got %d", len(trips))
	}
	if int(trips[0]["fix_count"].(float64)) != 5 {
		t.Fatalf("expected exactly 5 fixes (no double-insertion from the retry), got %v", trips[0]["fix_count"])
	}
}

// lossyOnceSender wraps a packetSender, making the FIRST call to Send
// succeed against the real underlying sender (so the aggregator actually
// commits it) but report a network error to the caller, simulating a lost
// response after a successful server-side commit. Subsequent calls pass
// through normally.
type lossyOnceSender struct {
	underlying packetSender
	usedOnce   bool
}

func (l *lossyOnceSender) Send(ctx context.Context, pkt *Packet) SendOutcome {
	real := l.underlying.Send(ctx, pkt)
	if !l.usedOnce {
		l.usedOnce = true
		// The real send above already committed on the aggregator; report a
		// network failure anyway to simulate the response being lost before
		// the simulator observed it.
		return SendOutcome{Failed: true, FailureKind: FailureKindNetworkError, AcceptanceUncertain: true, ErrorMessage: "simulated lost response"}
	}
	return real
}
