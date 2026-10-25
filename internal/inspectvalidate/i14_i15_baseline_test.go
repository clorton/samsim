package inspectvalidate

import (
	"os"
	"path/filepath"
	"testing"

	"gpsdemo/internal/inspectclient"
)

// TestI14_NoBaseline_YieldsExplicitTemporalSkip verifies V09/V10 are SKIP
// (not silently omitted or PASS) when no baseline is supplied, and the
// overall status is PASS_WITH_LIMITATIONS.
func TestI14_NoBaseline_YieldsExplicitTemporalSkip(t *testing.T) {
	obs := fx()
	report := BuildReport(obs, nil)
	if checkByID(report.Checks, "V09").Status != StatusSkip {
		t.Fatalf("expected V09 SKIP without a baseline, got %+v", checkByID(report.Checks, "V09"))
	}
	if checkByID(report.Checks, "V10").Status != StatusSkip {
		t.Fatalf("expected V10 SKIP without a baseline, got %+v", checkByID(report.Checks, "V10"))
	}
	if report.Overall != OverallPassWithLimitations {
		t.Fatalf("expected overall PASS_WITH_LIMITATIONS, got %s", report.Overall)
	}
}

// TestI14_FrozenGeometryChange_Fails verifies that a CLOSED trip's frozen
// geometry changing between baseline and current observation fails V09.
func TestI14_FrozenGeometryChange_Fails(t *testing.T) {
	obs := fx()
	baseline := ToBaseline(obs)

	obs2 := fx()
	obs2.Geometry["trip-manual"][0].Latitude += 1 // frozen trip's geometry changed
	report := BuildReport(obs2, baseline)
	if checkByID(report.Checks, "V09").Status != StatusFail {
		t.Fatalf("expected V09 FAIL on frozen geometry change, got %+v", checkByID(report.Checks, "V09"))
	}
}

// TestI14_FrozenMetadataChange_Fails verifies changing a frozen CLOSED
// trip's metadata (e.g. bounds) fails V09.
func TestI14_FrozenMetadataChange_Fails(t *testing.T) {
	obs := fx()
	baseline := ToBaseline(obs)

	obs2 := fx()
	i := findTripIndex(obs2, "trip-manual")
	obs2.Trips[i].ForcedClose = true // metadata field changed on a frozen trip
	report := BuildReport(obs2, baseline)
	if checkByID(report.Checks, "V09").Status != StatusFail {
		t.Fatalf("expected V09 FAIL on frozen metadata change, got %+v", checkByID(report.Checks, "V09"))
	}
}

// TestI14_MutableGrowth_Allowed verifies that a previously ACTIVE/INACTIVE
// trip growing (new fixes appended) between observations does not fail V09
// (only CLOSED trips are frozen).
func TestI14_MutableGrowth_Allowed(t *testing.T) {
	obs := fx()
	baseline := ToBaseline(obs)

	obs2 := fx()
	i := findTripIndex(obs2, "trip-mutable")
	obs2.Geometry["trip-mutable"] = append(obs2.Geometry["trip-mutable"], fixAtMs(702000))
	obs2.Trips[i].EndMs = 702000
	obs2.Trips[i].FixCount = 3
	obs2.Trips[i].UpdatedRevision = 6
	obs2.Revision = 6
	report := BuildReport(obs2, baseline)
	if checkByID(report.Checks, "V09").Status != StatusPass {
		t.Fatalf("expected V09 PASS when a mutable trip merely grows, got %+v", checkByID(report.Checks, "V09"))
	}
}

// TestI14_TerminalResolutionChange_Allowed verifies that the
// terminal_successor_trip_id field changing (as later merges extend the
// chain) is explicitly excluded from V09's frozen comparison.
func TestI14_TerminalResolutionChange_Allowed(t *testing.T) {
	obs := fx()
	baseline := ToBaseline(obs)

	obs2 := fx()
	i := findTripIndex(obs2, "trip-pred")
	newTerminal := "trip-succ-v2"
	obs2.Trips[i].TerminalSuccessorTripID = &newTerminal // legitimately changed
	report := BuildReport(obs2, baseline)
	if checkByID(report.Checks, "V09").Status != StatusPass {
		t.Fatalf("expected V09 PASS when only terminal_successor_trip_id changes, got %+v", checkByID(report.Checks, "V09"))
	}
}

// TestI14_RevisionMonotonic_Passes verifies V10 passes when revision and
// latest timestamp advance normally.
func TestI14_RevisionMonotonic_Passes(t *testing.T) {
	obs := fx()
	baseline := ToBaseline(obs)

	obs2 := fx()
	obs2.Revision = 6
	newLatest := inspectclient.FixDTO{TimestampMs: 1402000, Latitude: 42.002, Longitude: -75.002, SpeedMps: 3, HeadingDegrees: 30}
	obs2.LatestFix = &newLatest
	report := BuildReport(obs2, baseline)
	if checkByID(report.Checks, "V10").Status != StatusPass {
		t.Fatalf("expected V10 PASS on normal forward progress, got %+v", checkByID(report.Checks, "V10"))
	}
}

// TestI14_RevisionRegression_Fails verifies V10 fails if revision
// decreases.
func TestI14_RevisionRegression_Fails(t *testing.T) {
	obs := fx()
	baseline := ToBaseline(obs)

	obs2 := fx()
	obs2.Revision = 1 // regressed from baseline's 5
	report := BuildReport(obs2, baseline)
	if checkByID(report.Checks, "V10").Status != StatusFail {
		t.Fatalf("expected V10 FAIL on revision regression, got %+v", checkByID(report.Checks, "V10"))
	}
}

// TestI14_LatestTimestampRegression_Fails verifies V10 fails if the latest
// event timestamp regresses even with the same/advancing revision.
func TestI14_LatestTimestampRegression_Fails(t *testing.T) {
	obs := fx()
	baseline := ToBaseline(obs)

	obs2 := fx()
	obs2.Revision = 6
	regressed := inspectclient.FixDTO{TimestampMs: 1, Latitude: 1, Longitude: 1, SpeedMps: 0, HeadingDegrees: 0}
	obs2.LatestFix = &regressed
	report := BuildReport(obs2, baseline)
	if checkByID(report.Checks, "V10").Status != StatusFail {
		t.Fatalf("expected V10 FAIL on latest timestamp regression, got %+v", checkByID(report.Checks, "V10"))
	}
}

// TestI14_SegmentationConfigChange_Fails verifies changing trip_gap_seconds
// for the same database fails V10 (AG-01 forbids silent config changes).
func TestI14_SegmentationConfigChange_Fails(t *testing.T) {
	obs := fx()
	baseline := ToBaseline(obs)

	obs2 := fx()
	obs2.TripGapSeconds = 300
	report := BuildReport(obs2, baseline)
	if checkByID(report.Checks, "V10").Status != StatusFail {
		t.Fatalf("expected V10 FAIL on segmentation config change, got %+v", checkByID(report.Checks, "V10"))
	}
}

// --- I15: baseline file IO ---

// TestI15_WriteThenReadBaseline_RoundTrips verifies a written baseline can
// be read back and matches.
func TestI15_WriteThenReadBaseline_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")

	obs := fx()
	bl := ToBaseline(obs)
	if err := WriteBaselineExclusive(path, bl); err != nil {
		t.Fatalf("write baseline: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat baseline: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected baseline file mode 0600, got %o", info.Mode().Perm())
	}

	read, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if read.DatabaseID != bl.DatabaseID || read.VehicleID != bl.VehicleID || read.Revision != bl.Revision {
		t.Fatalf("round-tripped baseline does not match: %+v vs %+v", read, bl)
	}
}

// TestI15_RefusedOverwrite verifies WriteBaselineExclusive refuses to
// overwrite an existing file.
func TestI15_RefusedOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	obs := fx()
	bl := ToBaseline(obs)
	if err := WriteBaselineExclusive(path, bl); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteBaselineExclusive(path, bl); err == nil {
		t.Fatal("expected second write to the same path to fail (no silent overwrite)")
	}
}

// TestI15_MissingParentDirectory_Fails verifies a missing parent directory
// produces an explicit error, not a silent mkdir.
func TestI15_MissingParentDirectory_Fails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "baseline.json")
	obs := fx()
	bl := ToBaseline(obs)
	if err := WriteBaselineExclusive(path, bl); err == nil {
		t.Fatal("expected failure when parent directory does not exist")
	}
}

// TestI15_SizeLimit_Rejected verifies LoadBaseline rejects a file larger
// than the configured ceiling.
func TestI15_SizeLimit_Rejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Write more than MaxBaselineFileBytes of padding inside a JSON string
	// so it's syntactically plausible garbage, forcing the size check (not
	// a JSON parse error) to be what actually fails.
	f.WriteString(`{"schema_version":1,"database_id":"x","vehicle_id":"y","padding":"`)
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = 'a'
	}
	for written := 0; written < MaxBaselineFileBytes+(2<<20); written += len(chunk) {
		f.Write(chunk)
	}
	f.WriteString(`"}`)
	f.Close()

	_, err = LoadBaseline(path)
	if err == nil {
		t.Fatal("expected LoadBaseline to reject an oversized file")
	}
}

// TestI15_SchemaVersionMismatch_Rejected verifies an incompatible
// schema_version is rejected.
func TestI15_SchemaVersionMismatch_Rejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":999,"database_id":"x","vehicle_id":"y"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadBaseline(path)
	if err == nil {
		t.Fatal("expected schema_version mismatch to be rejected")
	}
}

// TestI15_MissingRequiredFields_Rejected verifies a baseline missing
// database_id/vehicle_id is rejected.
func TestI15_MissingRequiredFields_Rejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadBaseline(path)
	if err == nil {
		t.Fatal("expected missing required fields to be rejected")
	}
}

// TestI15_SavedObservationIsCoherent_NotPartial verifies ToBaseline/Write
// capture a full, single coherent observation's data (every trip has
// geometry recorded), not a partial subset.
func TestI15_SavedObservationIsCoherent_NotPartial(t *testing.T) {
	obs := fx()
	bl := ToBaseline(obs)
	if len(bl.Trips) != len(obs.Trips) {
		t.Fatalf("expected baseline to capture all %d trips, got %d", len(obs.Trips), len(bl.Trips))
	}
	for _, bt := range bl.Trips {
		if int64(len(bt.Geometry)) != bt.FixCount {
			t.Fatalf("trip %s: baseline geometry length %d does not match fix_count %d", bt.TripID, len(bt.Geometry), bt.FixCount)
		}
	}
}
