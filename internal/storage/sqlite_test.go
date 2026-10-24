package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), Options{
		Path:                  filepath.Join(dir, "test.sqlite"),
		BusyTimeoutMs:         5000,
		TripGapSeconds:        600,
		ExpectedFixIntervalMs: 1000,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpen_InitializesMetadata(t *testing.T) {
	s := openTestStore(t)
	if s.DatabaseID == "" {
		t.Fatal("expected nonempty database_id")
	}
	if s.TripGapSeconds != 600 || s.ExpectedFixIntervalMs != 1000 {
		t.Fatalf("unexpected config: %+v", s)
	}
}

func TestOpen_RejectsConflictingConfigOnReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sqlite")
	s, err := Open(context.Background(), Options{Path: path, BusyTimeoutMs: 5000, TripGapSeconds: 600, ExpectedFixIntervalMs: 1000})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	_, err = Open(context.Background(), Options{Path: path, BusyTimeoutMs: 5000, TripGapSeconds: 300, ExpectedFixIntervalMs: 1000})
	if err == nil {
		t.Fatal("expected error for conflicting trip_gap_seconds on reopen")
	}
}

func TestOpen_PreservesStateAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sqlite")
	s, err := Open(context.Background(), Options{Path: path, BusyTimeoutMs: 5000, TripGapSeconds: 600, ExpectedFixIntervalMs: 1000})
	if err != nil {
		t.Fatal(err)
	}
	dbID := s.DatabaseID

	ctx := context.Background()
	_, err = WithWriteTx(ctx, s, func(ctx context.Context, tx *sql.Tx) (struct{}, error) {
		if _, err := EnsureVehicle(ctx, tx, "vehicle-1", 1000); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(context.Background(), Options{Path: path, BusyTimeoutMs: 5000, TripGapSeconds: 600, ExpectedFixIntervalMs: 1000})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.DatabaseID != dbID {
		t.Fatalf("expected same database_id across restart, got %s vs %s", s2.DatabaseID, dbID)
	}
	v, err := ReadTx(ctx, s2, func(ctx context.Context, tx *sql.Tx) (*VehicleRow, error) {
		return GetVehicle(ctx, tx, "vehicle-1")
	})
	if err != nil {
		t.Fatalf("expected vehicle to survive restart: %v", err)
	}
	if v.VehicleID != "vehicle-1" {
		t.Fatalf("unexpected vehicle: %+v", v)
	}
}

func TestWithWriteTx_RollbackOnError(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := WithWriteTx(ctx, s, func(ctx context.Context, tx *sql.Tx) (struct{}, error) {
		if _, err := EnsureVehicle(ctx, tx, "vehicle-1", 1000); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, errFake
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}

	_, err = ReadTx(ctx, s, func(ctx context.Context, tx *sql.Tx) (*VehicleRow, error) {
		return GetVehicle(ctx, tx, "vehicle-1")
	})
	if err != ErrNotFound {
		t.Fatalf("expected rollback to prevent vehicle creation, got %v", err)
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (e *fakeErr) Error() string { return "fake error" }

func TestInsertAndListFixes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := WithWriteTx(ctx, s, func(ctx context.Context, tx *sql.Tx) (struct{}, error) {
		if _, err := EnsureVehicle(ctx, tx, "vehicle-1", 1000); err != nil {
			return struct{}{}, err
		}
		for _, ts := range []int64{3000, 1000, 2000} {
			if err := InsertFix(ctx, tx, FixRow{
				VehicleID: "vehicle-1", TimestampMs: ts, Latitude: 1, Longitude: 2,
				FirstSimulatorID: "sim-1", FirstPacketID: "20261024-1", ReceivedAtMs: 1000,
			}); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	fixes, err := ReadTx(ctx, s, func(ctx context.Context, tx *sql.Tx) ([]FixRow, error) {
		return ListFixesByVehicle(ctx, tx, "vehicle-1", nil, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fixes) != 3 {
		t.Fatalf("expected 3 fixes, got %d", len(fixes))
	}
	if fixes[0].TimestampMs != 1000 || fixes[1].TimestampMs != 2000 || fixes[2].TimestampMs != 3000 {
		t.Fatalf("expected chronological order, got %+v", fixes)
	}
}
