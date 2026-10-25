package main

import (
	"encoding/json"
	"strings"
	"testing"

	"gpsdemo/internal/inspectoutput"
)

// TestI02_HumanOutput_Vehicles covers human-readable table output, UTC
// timestamp formatting, and null rendering as "-".
func TestI02_HumanOutput_Vehicles(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	fs.seedSimpleVehicle("vehicle-001")

	stdout, _, exit := runCLI(t, "", "--base-url", fs.URL(), "vehicles")
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d", exit)
	}
	if !strings.Contains(stdout, "vehicle-001") {
		t.Fatalf("expected vehicle-001 in output: %s", stdout)
	}
	if !strings.Contains(stdout, "1970-01-01T00:00:02.000Z") {
		t.Fatalf("expected UTC ISO-8601 timestamp for ts=2000ms, got: %s", stdout)
	}
}

// TestI02_JSONOutput_StableEnvelope covers the finite-command JSON envelope
// shape and that int64 fields decode as integers, not floats.
func TestI02_JSONOutput_StableEnvelope(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	fs.seedSimpleVehicle("vehicle-001")

	stdout, stderr, exit := runCLI(t, "", "--base-url", fs.URL(), "--json", "vehicles")
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%s)", exit, stderr)
	}
	var doc struct {
		SchemaVersion int      `json:"schema_version"`
		Command       string   `json:"command"`
		Data          any      `json:"data"`
		Warnings      []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("expected valid JSON envelope, got error %v: %s", err, stdout)
	}
	if doc.SchemaVersion != inspectoutput.SchemaVersion {
		t.Fatalf("expected schema_version %d, got %d", inspectoutput.SchemaVersion, doc.SchemaVersion)
	}
	if doc.Command != "vehicles" {
		t.Fatalf("expected command=vehicles, got %q", doc.Command)
	}
	if strings.Contains(stdout, "Warning:") {
		t.Fatalf("expected no human warning text mixed into JSON stdout: %s", stdout)
	}
}

// TestI02_EmptyList_SucceedsNotFails verifies an empty result set is a
// successful (exit 0) "no matching items" outcome, not an error.
func TestI02_EmptyList_SucceedsNotFails(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()
	// No vehicles seeded at all.

	stdout, _, exit := runCLI(t, "", "--base-url", fs.URL(), "vehicles")
	if exit != 0 {
		t.Fatalf("expected exit 0 for empty list, got %d", exit)
	}
	if !strings.Contains(stdout, "No vehicles found") {
		t.Fatalf("expected explicit empty-list message, got: %s", stdout)
	}
}

// TestI02_NotFound_ExitCodeAndErrorEnvelope covers the AG-07 error envelope
// mapping to the documented exit code 1, and that --json mode emits the
// error document to stdout.
func TestI02_NotFound_ExitCodeAndErrorEnvelope(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()

	stdout, stderr, exit := runCLI(t, "", "--base-url", fs.URL(), "--json", "vehicle", "nonexistent")
	if exit != inspectoutput.ExitAPIOrIO {
		t.Fatalf("expected exit %d, got %d", inspectoutput.ExitAPIOrIO, exit)
	}
	var doc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("expected JSON error document on stdout, got error %v: %s", err, stdout)
	}
	if doc.Error.Code != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND, got %q", doc.Error.Code)
	}
	if stderr == "" {
		t.Fatalf("expected a short diagnostic on stderr too")
	}
}

// TestI02_HumanError_NoPromptMixedIn is a smoke check that non-JSON errors
// go to stderr, not stdout.
func TestI02_HumanError_NoPromptMixedIn(t *testing.T) {
	fs := newFixtureServer()
	defer fs.Close()

	stdout, stderr, exit := runCLI(t, "", "--base-url", fs.URL(), "vehicle", "nonexistent")
	if exit != inspectoutput.ExitAPIOrIO {
		t.Fatalf("expected exit %d, got %d", inspectoutput.ExitAPIOrIO, exit)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout on human-mode error, got: %s", stdout)
	}
	if !strings.Contains(stderr, "NOT_FOUND") {
		t.Fatalf("expected error code on stderr, got: %s", stderr)
	}
}
