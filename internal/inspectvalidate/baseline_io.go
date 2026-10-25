package inspectvalidate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MaxBaselineFileBytes is the default baseline file size ceiling (INS-05).
const MaxBaselineFileBytes = 64 << 20

// ErrBaselineDatabaseMismatch is returned by LoadBaseline's caller-side
// check (performed by the command layer, which knows the current
// observation's database_id) -- declared here for a single canonical
// message string.
var ErrBaselineDatabaseMismatch = fmt.Errorf("BASELINE_DATABASE_MISMATCH")

// LoadBaseline reads and validates a baseline file from path, enforcing the
// size ceiling and schema version, and rejecting malformed values.
func LoadBaseline(path string) (*Baseline, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open baseline: %w", err)
	}
	defer f.Close()

	limited := io.LimitReader(f, MaxBaselineFileBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read baseline: %w", err)
	}
	if int64(len(data)) > MaxBaselineFileBytes {
		return nil, fmt.Errorf("baseline file exceeds the %d byte limit", MaxBaselineFileBytes)
	}

	var bl Baseline
	if err := json.Unmarshal(data, &bl); err != nil {
		return nil, fmt.Errorf("malformed baseline file: %w", err)
	}
	if bl.SchemaVersion != BaselineSchemaVersion {
		return nil, fmt.Errorf("unsupported baseline schema_version %d (expected %d)", bl.SchemaVersion, BaselineSchemaVersion)
	}
	if bl.DatabaseID == "" || bl.VehicleID == "" {
		return nil, fmt.Errorf("baseline file is missing required database_id/vehicle_id fields")
	}
	return &bl, nil
}

// WriteBaselineExclusive atomically creates a new baseline file at path,
// refusing to overwrite an existing file (INS-05: "Create a new file
// exclusively (0600 where supported), no silent overwrite; write to
// same-directory temporary file and install atomically using a no-replace
// strategy"). The parent directory must already exist.
func WriteBaselineExclusive(path string, bl *Baseline) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("baseline parent directory %q does not exist: %w", dir, err)
	}

	// Fail fast (before even writing a temp file) if the destination
	// already exists, for a clearer error than a late rename failure.
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("baseline file %q already exists; refusing to overwrite", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat baseline destination: %w", err)
	}

	data, err := json.MarshalIndent(bl, "", "  ")
	if err != nil {
		return fmt.Errorf("encode baseline: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".baseline-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp baseline file: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp baseline file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp baseline file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp baseline file: %w", err)
	}

	if err := linkNoReplace(tmpPath, path); err != nil {
		return fmt.Errorf("install baseline file: %w", err)
	}
	success = true
	return nil
}
