package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gpsdemo/internal/inspectoutput"
)

// TestI15_BaselineDatabaseMismatch_ViaCLI verifies that running validate
// with a baseline from a different database_id fails explicitly rather
// than silently comparing unrelated state.
func TestI15_BaselineDatabaseMismatch_ViaCLI(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	fs.seedSimpleVehicle("vehicle-001")

	dir := t.TempDir()
	baselinePath := filepath.Join(dir, "baseline.json")
	if err := os.WriteFile(baselinePath, []byte(`{"schema_version":1,"database_id":"some-other-db","vehicle_id":"vehicle-001","revision":1,"trips":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, exit := runCLI(t, "", "--base-url", fs.URL(), "validate", "vehicle-001", "--baseline", baselinePath)
	if exit != inspectoutput.ExitAPIOrIO {
		t.Fatalf("expected exit %d, got %d (stderr=%s)", inspectoutput.ExitAPIOrIO, exit, stderr)
	}
	if !strings.Contains(stderr, "BASELINE_DATABASE_MISMATCH") {
		t.Fatalf("expected BASELINE_DATABASE_MISMATCH in stderr, got: %s", stderr)
	}
}

// TestI15_WriteBaseline_ViaCLI_CreatesFile verifies --write-baseline
// produces a readable file after a passing validate run.
func TestI15_WriteBaseline_ViaCLI_CreatesFile(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	fs.seedSimpleVehicle("vehicle-001")

	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.json")

	_, stderr, exit := runCLI(t, "", "--base-url", fs.URL(), "validate", "vehicle-001", "--write-baseline", outPath)
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%s)", exit, stderr)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected baseline file to be created: %v", err)
	}
}

// TestI15_ValidateExitCode3_OnFail verifies the documented exit code 3 when
// a validate report contains a FAIL check, using a deliberately broken
// fixture server response (vehicle's latest_fix disagrees with its own raw
// fix history).
func TestI15_ValidateExitCode3_OnFail(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	tripID := fs.seedSimpleVehicle("vehicle-001")

	fs.mu.Lock()
	// Corrupt latest_fix so it disagrees with the max-timestamp raw fix,
	// forcing V01 to fail.
	v := fs.vehicles["vehicle-001"]
	corrupted := map[string]any{}
	for k, val := range v.LatestFix {
		corrupted[k] = val
	}
	corrupted["latitude"] = 999.0
	v.LatestFix = corrupted
	_ = tripID
	fs.mu.Unlock()

	_, stderr, exit := runCLI(t, "", "--base-url", fs.URL(), "validate", "vehicle-001")
	if exit != inspectoutput.ExitValidationFailed {
		t.Fatalf("expected exit %d, got %d (stderr=%s)", inspectoutput.ExitValidationFailed, exit, stderr)
	}
}
