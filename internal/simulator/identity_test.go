package simulator

import (
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestReserveNextSequence_FreshStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	store, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("OpenIdentityStore: %v", err)
	}
	defer store.Close()

	for want := int64(1); want <= 5; want++ {
		got, err := store.ReserveNextSequence("sim-1", "20261024")
		if err != nil {
			t.Fatalf("ReserveNextSequence: %v", err)
		}
		if got != want {
			t.Fatalf("ReserveNextSequence = %d, want %d", got, want)
		}
	}
}

func TestReserveNextSequence_IndependentPerDate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	store, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("OpenIdentityStore: %v", err)
	}
	defer store.Close()

	for i := 0; i < 3; i++ {
		if _, err := store.ReserveNextSequence("sim-1", "20261024"); err != nil {
			t.Fatalf("ReserveNextSequence: %v", err)
		}
	}

	got, err := store.ReserveNextSequence("sim-1", "20261025")
	if err != nil {
		t.Fatalf("ReserveNextSequence: %v", err)
	}
	if got != 1 {
		t.Fatalf("ReserveNextSequence for new date = %d, want 1", got)
	}

	// Original date's counter must be unaffected.
	got, err = store.ReserveNextSequence("sim-1", "20261024")
	if err != nil {
		t.Fatalf("ReserveNextSequence: %v", err)
	}
	if got != 4 {
		t.Fatalf("ReserveNextSequence for original date = %d, want 4", got)
	}
}

func TestReserveNextSequence_IndependentPerSimulatorID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	store, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("OpenIdentityStore: %v", err)
	}
	defer store.Close()

	for i := 0; i < 3; i++ {
		if _, err := store.ReserveNextSequence("sim-A", "20261024"); err != nil {
			t.Fatalf("ReserveNextSequence: %v", err)
		}
	}

	got, err := store.ReserveNextSequence("sim-B", "20261024")
	if err != nil {
		t.Fatalf("ReserveNextSequence: %v", err)
	}
	if got != 1 {
		t.Fatalf("ReserveNextSequence for new simulator = %d, want 1", got)
	}

	got, err = store.ReserveNextSequence("sim-A", "20261024")
	if err != nil {
		t.Fatalf("ReserveNextSequence: %v", err)
	}
	if got != 4 {
		t.Fatalf("ReserveNextSequence for original simulator = %d, want 4", got)
	}
}

func TestReserveNextSequence_RestartDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")

	store, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("OpenIdentityStore: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.ReserveNextSequence("sim-1", "20261024"); err != nil {
			t.Fatalf("ReserveNextSequence: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("reopen OpenIdentityStore: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.ReserveNextSequence("sim-1", "20261024")
	if err != nil {
		t.Fatalf("ReserveNextSequence after reopen: %v", err)
	}
	if got != 4 {
		t.Fatalf("ReserveNextSequence after reopen = %d, want 4 (continuing, not resetting or reusing)", got)
	}
}

func TestReserveNextSequence_WallClockRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	store, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("OpenIdentityStore: %v", err)
	}
	defer store.Close()

	// Reserve a few sequences on the later date first.
	for i := 0; i < 3; i++ {
		if _, err := store.ReserveNextSequence("sim-1", "20261025"); err != nil {
			t.Fatalf("ReserveNextSequence: %v", err)
		}
	}

	// Rolling back to an earlier, never-used date starts at 1.
	got, err := store.ReserveNextSequence("sim-1", "20261024")
	if err != nil {
		t.Fatalf("ReserveNextSequence: %v", err)
	}
	if got != 1 {
		t.Fatalf("ReserveNextSequence for earlier never-used date = %d, want 1", got)
	}

	// Reserve a couple more on the earlier date.
	got, err = store.ReserveNextSequence("sim-1", "20261024")
	if err != nil {
		t.Fatalf("ReserveNextSequence: %v", err)
	}
	if got != 2 {
		t.Fatalf("ReserveNextSequence for earlier date = %d, want 2", got)
	}

	// Jumping forward again must resume the later date's own counter (4),
	// not be influenced by what happened on the earlier date.
	got, err = store.ReserveNextSequence("sim-1", "20261025")
	if err != nil {
		t.Fatalf("ReserveNextSequence: %v", err)
	}
	if got != 4 {
		t.Fatalf("ReserveNextSequence for later date after rollback-and-forward = %d, want 4", got)
	}

	// Rolling back to the earlier date again continues from its own
	// persisted counter (3), not from zero and not from the later date's
	// counter.
	got, err = store.ReserveNextSequence("sim-1", "20261024")
	if err != nil {
		t.Fatalf("ReserveNextSequence: %v", err)
	}
	if got != 3 {
		t.Fatalf("ReserveNextSequence for earlier date again = %d, want 3", got)
	}
}

func TestOpenIdentityStore_ExclusiveInstanceLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")

	store1, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("first OpenIdentityStore: %v", err)
	}

	_, err = OpenIdentityStore(path)
	if err == nil {
		t.Fatal("second OpenIdentityStore while first is open: expected error, got nil")
	}

	if err := store1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("OpenIdentityStore after Close: %v", err)
	}
	defer store2.Close()

	// Sanity: the reopened store should still be functional and continue
	// counters from where store1 left off (store1 never reserved anything,
	// so this should start at 1).
	got, err := store2.ReserveNextSequence("sim-1", "20261024")
	if err != nil {
		t.Fatalf("ReserveNextSequence: %v", err)
	}
	if got != 1 {
		t.Fatalf("ReserveNextSequence = %d, want 1", got)
	}
}

func TestFormatPacketID(t *testing.T) {
	cases := []struct {
		utcDate  string
		sequence int64
		want     string
	}{
		{"20261024", 1, "20261024-000001"},
		{"20261024", 999999, "20261024-999999"},
		{"20261024", 1000000, "20261024-1000000"},
		{"20261024", 0, "20261024-000000"},
	}
	for _, c := range cases {
		got := FormatPacketID(c.utcDate, c.sequence)
		if got != c.want {
			t.Errorf("FormatPacketID(%q, %d) = %q, want %q", c.utcDate, c.sequence, got, c.want)
		}
	}
}

func TestReserveNextSequence_ConcurrentUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	store, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("OpenIdentityStore: %v", err)
	}
	defer store.Close()

	const n = 50
	results := make([]int64, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			seq, err := store.ReserveNextSequence("sim-1", "20261024")
			results[i] = seq
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: ReserveNextSequence: %v", i, err)
		}
	}

	sorted := append([]int64(nil), results...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for i, v := range sorted {
		want := int64(i + 1)
		if v != want {
			t.Fatalf("concurrent reservations not contiguous/unique: sorted[%d] = %d, want %d (full sorted list: %v)", i, v, want, sorted)
		}
	}
}

func TestOpenIdentityStore_MissingParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "nested", "identity.db")
	_, err := OpenIdentityStore(path)
	if err == nil {
		t.Fatal("OpenIdentityStore with missing parent directory: expected error, got nil")
	}
}

func TestUTCDateString(t *testing.T) {
	// Exercise via FormatPacketID/ReserveNextSequence round trip: a value
	// produced by UTCDateString must be accepted as a valid utcDate.
	path := filepath.Join(t.TempDir(), "identity.db")
	store, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatalf("OpenIdentityStore: %v", err)
	}
	defer store.Close()

	t1, err := time.Parse(time.RFC3339, "2026-10-24T23:59:59Z")
	if err != nil {
		t.Fatalf("time.Parse: %v", err)
	}
	date := UTCDateString(t1)
	if date != "20261024" {
		t.Fatalf("UTCDateString = %q, want %q", date, "20261024")
	}

	if _, err := store.ReserveNextSequence("sim-1", date); err != nil {
		t.Fatalf("ReserveNextSequence with UTCDateString output: %v", err)
	}

	// A non-UTC time should be normalized to UTC before formatting.
	t2, err := time.Parse(time.RFC3339, "2026-10-25T01:30:00-05:00") // 06:30 UTC on 2026-10-25
	if err != nil {
		t.Fatalf("time.Parse: %v", err)
	}
	date2 := UTCDateString(t2)
	if date2 != "20261025" {
		t.Fatalf("UTCDateString (non-UTC input) = %q, want %q", date2, "20261025")
	}
}
