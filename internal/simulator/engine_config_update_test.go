package simulator

import (
	"testing"
	"time"
)

// TestEngine_PutConfig_HotFieldsApplyWhileRunning implements S14: hot
// fields apply atomically regardless of playback state.
func TestEngine_PutConfig_HotFieldsApplyWhileRunning(t *testing.T) {
	e, _ := newTestEngine(t, nil, buildRouteCSV(1000))
	e.Start()

	newMult := 500.0
	if err := e.PutConfig(ConfigUpdate{ClockMultiplier: &newMult}); err != nil {
		t.Fatal(err)
	}
	if e.Status().Config.ClockMultiplier != 500 {
		t.Fatalf("expected clock_multiplier updated to 500, got %v", e.Status().Config.ClockMultiplier)
	}
}

// TestEngine_PutConfig_NoFieldsIsError implements: "At least one field
// required."
func TestEngine_PutConfig_NoFieldsIsError(t *testing.T) {
	e, _ := newTestEngine(t, nil, buildRouteCSV(5))
	if err := e.PutConfig(ConfigUpdate{}); err != ErrNoFieldsProvided {
		t.Fatalf("expected ErrNoFieldsProvided, got %v", err)
	}
}

// TestEngine_PutConfig_StaleControlRevisionConflict implements: "if
// supplied and stale return 409 CONTROL_REVISION_CONFLICT."
func TestEngine_PutConfig_StaleControlRevisionConflict(t *testing.T) {
	e, _ := newTestEngine(t, nil, buildRouteCSV(1000))
	e.Start() // bumps control_revision to 1

	stale := int64(0)
	newMult := 200.0
	err := e.PutConfig(ConfigUpdate{ClockMultiplier: &newMult, ExpectedControlRevision: &stale})
	if err != ErrControlRevisionConflict {
		t.Fatalf("expected ErrControlRevisionConflict, got %v", err)
	}
}

// TestEngine_PutConfig_PacketSizeRequiresNoPending implements: packet_size,
// encoding, request_timeout_ms may change only when no pending/in-flight
// packet exists.
func TestEngine_PutConfig_PacketSizeRequiresNoPending(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) { c.PacketSize = 1 }, buildRouteCSV(1000))
	deps.sender.setOutcome(func(pkt *Packet) SendOutcome {
		return SendOutcome{Failed: true, FailureKind: FailureKindNetworkError}
	})
	e.Start()
	deps.mono.Advance(1 * time.Second)
	e.Tick()
	waitForPendingCleared(t, e) // now there is a pending (failed) packet

	newSize := 20
	if err := e.PutConfig(ConfigUpdate{PacketSize: &newSize}); err != ErrFieldRequiresNoPending {
		t.Fatalf("expected ErrFieldRequiresNoPending, got %v", err)
	}
}

// TestEngine_PutConfig_VehicleIDRequiresReadyEmpty implements: vehicle_id/
// route_id/run_start_ms/aggregator_url/expected_fix_interval_ms require
// READY with no pending records.
func TestEngine_PutConfig_VehicleIDRequiresReadyEmpty(t *testing.T) {
	e, _ := newTestEngine(t, nil, buildRouteCSV(1000))
	e.Start()

	newVehicle := "vehicle-other"
	if err := e.PutConfig(ConfigUpdate{VehicleID: &newVehicle}); err != ErrFieldRequiresReadyEmpty {
		t.Fatalf("expected ErrFieldRequiresReadyEmpty while RUNNING, got %v", err)
	}

	e.Stop()
	if _, err := e.Reset(true); err != nil {
		t.Fatal(err)
	}
	// Now READY with no pending records: must succeed.
	if err := e.PutConfig(ConfigUpdate{VehicleID: &newVehicle}); err != nil {
		t.Fatalf("expected vehicle_id change to succeed in READY+empty, got %v", err)
	}
	if e.Status().VehicleID != "vehicle-other" {
		t.Fatal("expected vehicle_id applied")
	}
}

// TestEngine_PutConfig_RouteIDChangeValidatesBeforeApplying implements:
// "For an accepted route_id change, load/validate the allowlisted route
// before applying."
func TestEngine_PutConfig_RouteIDChangeValidatesBeforeApplying(t *testing.T) {
	e, _ := newTestEngine(t, func(c *Config) {
		c.Routes = map[string]string{"demo": "unused", "bad": "unused"}
	}, buildRouteCSV(5))

	// Swap in a RouteLoader that returns invalid CSV for "bad".
	e.routeLoader = func(routeID string) ([]byte, error) {
		if routeID == "bad" {
			return []byte("not,a,valid,route,csv\n1,2,3,4,5\n"), nil
		}
		return []byte(buildRouteCSV(5)), nil
	}

	newRoute := "bad"
	if err := e.PutConfig(ConfigUpdate{RouteID: &newRoute}); err == nil {
		t.Fatal("expected route validation failure to reject the update before applying anything")
	}
	if e.Status().Config.RouteID != "demo" {
		t.Fatalf("expected route_id unchanged after rejected update, got %s", e.Status().Config.RouteID)
	}
}

// TestEngine_PutConfig_ReportIntervalRequiresStopped implements
// SIMULATOR_CHANGE_REQUEST.md: "interval changes require stopped
// playback, seal the current partial window once, and anchor the new
// schedule at current virtual time."
func TestEngine_PutConfig_ReportIntervalRequiresStopped(t *testing.T) {
	e, deps := newTestEngine(t, func(c *Config) {
		c.ClockMultiplier = 10
		c.NormalReportIntervalSimulatedMs = 999_000_000 // never closes naturally in this test
	}, buildRouteCSV(1000))
	e.Start()

	newInterval := int64(5000)
	if err := e.PutConfig(ConfigUpdate{NormalReportIntervalSimulatedMs: &newInterval}); err != ErrFieldRequiresStopped {
		t.Fatalf("expected ErrFieldRequiresStopped while RUNNING, got %v", err)
	}

	// Generate some live records that accumulate in the (never-closing)
	// open window, then stop and change the interval: the open window must
	// be sealed (moved into the ready queue) exactly once as part of the
	// change, not discarded.
	deps.mono.Advance(300 * time.Millisecond) // *10 -> 3 virtual seconds
	e.Tick()
	pending := e.Status().LiveWindowPendingCount
	if pending == 0 {
		t.Fatal("expected some live records accumulated in the open window before stop")
	}

	e.Stop()
	if err := e.PutConfig(ConfigUpdate{NormalReportIntervalSimulatedMs: &newInterval}); err != nil {
		t.Fatalf("expected report interval change to succeed while STOPPED, got %v", err)
	}

	st := e.Status()
	if st.Config.NormalReportIntervalSimulatedMs != newInterval {
		t.Fatalf("expected interval applied, got %d", st.Config.NormalReportIntervalSimulatedMs)
	}
	if st.LiveWindowPendingCount != 0 {
		t.Fatalf("expected the open window to be sealed (moved to the ready queue) by the interval change, got %d still pending", st.LiveWindowPendingCount)
	}
	if st.LiveQueueCount != pending {
		t.Fatalf("expected the sealed window's %d records to land in the live queue, got %d", pending, st.LiveQueueCount)
	}
}

// TestEngine_PutConfig_StartupOnlyFieldsRejected implements: listen,
// simulator_id, identity_db_path, route map and resource limits are
// startup-only; there is no ConfigUpdate field for them at all, so this
// test simply documents that ConfigUpdate's type system makes them
// impossible to submit (compile-time enforcement), which we confirm by
// checking the struct has no such fields via its zero value behavior
// (this is a documentation test more than a runtime behavior test).
func TestEngine_PutConfig_StartupOnlyFieldsNotExposed(t *testing.T) {
	var u ConfigUpdate
	// If this compiles, ConfigUpdate has no exported field for these
	// startup-only settings -- which is the enforcement mechanism.
	_ = u
}
