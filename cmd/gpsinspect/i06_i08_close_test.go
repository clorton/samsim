package main

import (
	"strings"
	"testing"

	"gpsdemo/internal/inspectoutput"
)

// TestI06_CompleteTrip_SendsExpectedRevisionAndForceFalse verifies the
// close workflow for a no-gap trip: no prompt, a single POST with the
// pinned revision and force=false.
func TestI06_CompleteTrip_SendsExpectedRevisionAndForceFalse(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedSimpleVehicle("vehicle-001")

	var gotBody map[string]any
	var callCount int
	fs.closeHandler = func(id string, body map[string]any) (int, map[string]any) {
		callCount++
		gotBody = body
		return 200, map[string]any{
			"trip_id": id, "status": "CLOSED", "close_reason": "MANUAL", "revision": int64(2), "forced_close": false,
		}
	}

	stdout, stderr, exit := runCLI(t, "", "--base-url", fs.URL(), "close", tripID)
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%s)", exit, stderr)
	}
	if callCount != 1 {
		t.Fatalf("expected exactly 1 close POST, got %d", callCount)
	}
	if gotBody["expected_revision"].(float64) != 1 {
		t.Fatalf("expected expected_revision=1, got %v", gotBody["expected_revision"])
	}
	if gotBody["force"].(bool) != false {
		t.Fatalf("expected force=false for a complete trip, got %v", gotBody["force"])
	}
	if !strings.Contains(stdout, "CLOSED") {
		t.Fatalf("expected closed confirmation in output: %s", stdout)
	}
}

// TestI06_IncompleteNoninteractive_SendsNoPOST verifies that a trip with
// gaps, run noninteractively without --force, sends no POST at all and
// returns exit code 4.
func TestI06_IncompleteNoninteractive_SendsNoPOST(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedVehicleWithGaps("vehicle-001")

	called := false
	fs.closeHandler = func(id string, body map[string]any) (int, map[string]any) {
		called = true
		return 200, map[string]any{}
	}

	_, stderr, exit := runCLI(t, "", "--base-url", fs.URL(), "close", tripID)
	if exit != inspectoutput.ExitCloseCanceled {
		t.Fatalf("expected exit %d, got %d (stderr=%s)", inspectoutput.ExitCloseCanceled, exit, stderr)
	}
	if called {
		t.Fatal("expected no POST to be sent")
	}
}

// TestI06_Interactive_No_SendsNoPOST verifies that answering "n" (or
// anything but y/yes) at the confirmation prompt sends no POST.
func TestI06_Interactive_No_SendsNoPOST(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedVehicleWithGaps("vehicle-001")

	called := false
	fs.closeHandler = func(id string, body map[string]any) (int, map[string]any) {
		called = true
		return 200, map[string]any{}
	}

	// Interactive mode requires StdinIsTerminal/StderrIsTerminal, which the
	// real run() only sets from *os.File; exercise the close command
	// directly against inspector.App instead to simulate "terminal" IO
	// without a real pty, matching INS-01's "Tests must not require an
	// interactive terminal."
	app, ctx := newTestApp(t, fs.URL(), withTerminals(true, true), withStdin("n\n"))
	report, err := runCloseDirect(ctx, app, tripID, false)
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if report.Outcome != "CANCELED" {
		t.Fatalf("expected CANCELED outcome, got %s", report.Outcome)
	}
	if called {
		t.Fatal("expected no POST to be sent")
	}
}

// TestI06_Interactive_EOF_SendsNoPOST verifies EOF at the prompt cancels.
func TestI06_Interactive_EOF_SendsNoPOST(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedVehicleWithGaps("vehicle-001")

	called := false
	fs.closeHandler = func(id string, body map[string]any) (int, map[string]any) {
		called = true
		return 200, map[string]any{}
	}

	app, ctx := newTestApp(t, fs.URL(), withTerminals(true, true), withStdin(""))
	report, err := runCloseDirect(ctx, app, tripID, false)
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if report.Outcome != "CANCELED" {
		t.Fatalf("expected CANCELED outcome, got %s", report.Outcome)
	}
	if called {
		t.Fatal("expected no POST to be sent")
	}
}

// TestI07_IncompleteYes_SendsForceTrue verifies interactive "yes"
// confirmation results in force=true.
func TestI07_IncompleteYes_SendsForceTrue(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedVehicleWithGaps("vehicle-001")

	var gotForce bool
	fs.closeHandler = func(id string, body map[string]any) (int, map[string]any) {
		gotForce = body["force"].(bool)
		return 200, map[string]any{"trip_id": id, "status": "CLOSED", "close_reason": "MANUAL", "revision": int64(2), "forced_close": true}
	}

	app, ctx := newTestApp(t, fs.URL(), withTerminals(true, true), withStdin("yes\n"))
	report, err := runCloseDirect(ctx, app, tripID, false)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if report.Outcome != "CLOSED" {
		t.Fatalf("expected CLOSED, got %s", report.Outcome)
	}
	if !gotForce {
		t.Fatal("expected force=true after explicit yes confirmation")
	}
}

// TestI07_ForceFlag_SendsForceTrueWithoutPrompt verifies --force skips the
// prompt entirely.
func TestI07_ForceFlag_SendsForceTrueWithoutPrompt(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedVehicleWithGaps("vehicle-001")

	var gotForce bool
	fs.closeHandler = func(id string, body map[string]any) (int, map[string]any) {
		gotForce = body["force"].(bool)
		return 200, map[string]any{"trip_id": id, "status": "CLOSED", "close_reason": "MANUAL", "revision": int64(2), "forced_close": true}
	}

	// --force must work even noninteractively (no terminal at all).
	app, ctx := newTestApp(t, fs.URL(), withTerminals(false, false))
	report, err := runCloseDirect(ctx, app, tripID, true)
	if err != nil {
		t.Fatalf("expected success with --force, got error: %v", err)
	}
	if report.Outcome != "CLOSED" {
		t.Fatalf("expected CLOSED, got %s", report.Outcome)
	}
	if !gotForce {
		t.Fatal("expected force=true with --force flag")
	}
}

// TestI07_SupersededTarget_NeverRedirected verifies closing a CLOSED/MERGED
// trip fails with TRIP_SUPERSEDED and never POSTs.
func TestI07_SupersededTarget_NeverRedirected(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	predID, succID := fs.seedMergedLineage("vehicle-001")

	called := false
	fs.closeHandler = func(id string, body map[string]any) (int, map[string]any) {
		called = true
		return 200, map[string]any{}
	}

	_, stderr, exit := runCLI(t, "", "--base-url", fs.URL(), "close", predID)
	if exit != inspectoutput.ExitAPIOrIO {
		t.Fatalf("expected exit %d, got %d (stderr=%s)", inspectoutput.ExitAPIOrIO, exit, stderr)
	}
	if called {
		t.Fatal("expected no POST for a superseded trip")
	}
	if !strings.Contains(stderr, succID) {
		t.Fatalf("expected successor ID %s mentioned, got: %s", succID, stderr)
	}
}

// TestI08_AlreadyClosedPreflight_SucceedsWithoutPOST verifies a
// CLOSED/MANUAL trip reports success without ever sending a POST.
func TestI08_AlreadyClosedPreflight_SucceedsWithoutPOST(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedSimpleVehicle("vehicle-001")

	fs.mu.Lock()
	ft := fs.trips[tripID]
	ft.Summary["status"] = "CLOSED"
	ft.Summary["close_reason"] = "MANUAL"
	ft.Detail["status"] = "CLOSED"
	ft.Detail["close_reason"] = "MANUAL"
	fs.mu.Unlock()

	called := false
	fs.closeHandler = func(id string, body map[string]any) (int, map[string]any) {
		called = true
		return 200, map[string]any{}
	}

	stdout, _, exit := runCLI(t, "", "--base-url", fs.URL(), "close", tripID)
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d: %s", exit, stdout)
	}
	if called {
		t.Fatal("expected no POST for an already-closed trip")
	}
	if !strings.Contains(stdout, "ALREADY_CLOSED") {
		t.Fatalf("expected ALREADY_CLOSED outcome, got: %s", stdout)
	}
}

// TestI08_LostResponse_ReconcilesViaGET verifies that a network-level
// failure during the close POST, followed by the trip actually being
// CLOSED/MANUAL on reread, is reported as an observed closure rather than
// an error -- and that no second POST is ever sent.
func TestI08_LostResponse_ReconcilesViaGET(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedSimpleVehicle("vehicle-001")

	var callCount int
	fs.closeHandler = func(id string, body map[string]any) (int, map[string]any) {
		callCount++
		// Simulate the close having actually landed server-side, but the
		// response never reaching the client: mark CLOSED here, then return
		// a connection-reset-like failure by writing invalid JSON the
		// client can't parse as either a success or a valid error envelope.
		fs.mu.Lock()
		ft := fs.trips[id]
		ft.Summary["status"] = "CLOSED"
		ft.Summary["close_reason"] = "MANUAL"
		ft.Detail["status"] = "CLOSED"
		ft.Detail["close_reason"] = "MANUAL"
		fs.mu.Unlock()
		return 599, map[string]any{} // triggers a protocol-level error client-side
	}

	stdout, _, exit := runCLI(t, "", "--base-url", fs.URL(), "close", tripID)
	if exit != 0 {
		t.Fatalf("expected exit 0 (closure observed via reconciliation), got %d: %s", exit, stdout)
	}
	if callCount != 1 {
		t.Fatalf("expected exactly 1 POST attempt (no blind retry), got %d", callCount)
	}
	if !strings.Contains(stdout, "ALREADY_CLOSED") {
		t.Fatalf("expected ALREADY_CLOSED (observed) outcome, got: %s", stdout)
	}
}
