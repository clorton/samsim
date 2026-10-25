// This file implements SIM-03's durable packet-ID counter store: a
// dedicated SQLite-backed sequence allocator scoped to (simulator_id, UTC
// date), independent of any aggregator storage code.
//
// This package has zero dependency on any aggregator-specific package
// (internal/apierr, internal/storage, internal/aggregator, wire code, etc).
// It only depends on the Go standard library plus the pure-Go SQLite driver.
package simulator

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// IdentityStore durably allocates packet sequence numbers scoped to
// (simulator_id, UTC date), per SIM-03. It is backed by a dedicated SQLite
// file, independent of the aggregator's database.
type IdentityStore struct {
	db   *sql.DB
	mu   sync.Mutex // serializes reserve-next-sequence read-modify-write across goroutines in this process
	lock *instanceLock
}

// identityDSN builds the SQLite DSN for the identity store file. We enable
// foreign_keys and a busy_timeout pragma for consistency with the rest of
// the project's SQLite style (see internal/storage/sqlite.go). Unlike the
// aggregator's database, this file is low-stakes, single-writer, and
// low-volume (one row read-modify-write per packet at most), so we don't
// bother with WAL or synchronous=FULL: the default rollback-journal mode
// combined with our own exclusive instance lock (see OpenIdentityStore) is
// simpler and sufficient here.
func identityDSN(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
}

// OpenIdentityStore opens (creating if necessary) the identity store at
// path, acquiring an exclusive instance lock, and returns a ready
// *IdentityStore.
//
// Locking strategy: this uses a sibling lock file (path + ".lock") created
// with os.O_CREATE|os.O_EXCL, containing the current process's PID. This is
// a simple, portable approach that works identically across platforms with
// the pure-Go SQLite driver, without relying on SQLite's own
// (platform/VFS-dependent) file-locking semantics.
//
// Limitation: if a process holding the lock is killed without a chance to
// run deferred cleanup (e.g. SIGKILL, power loss), the lock file is left
// behind and every subsequent OpenIdentityStore call against the same path
// will fail with a "locked by pid N" error, even though pid N is no longer
// running. This implementation intentionally does not attempt to detect or
// reap stale locks (doing so reliably and portably, e.g. distinguishing a
// dead PID from a live unrelated process that happens to reuse the PID, is
// its own can of worms). Recovering from this requires an operator to
// manually delete the ".lock" file after confirming the previous process is
// in fact no longer running. Similarly, deleting or replacing the identity
// store's underlying database file while continuing to reuse the same
// simulator_id is unsupported and will silently restart that simulator's
// counters from scratch; if the store is lost, operators should mint and
// use a new simulator identity rather than reusing the old one.
func OpenIdentityStore(path string) (*IdentityStore, error) {
	if dir := filepath.Dir(path); dir != "." {
		if info, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("simulator: identity store: parent directory %q does not exist or is inaccessible: %w", dir, err)
		} else if !info.IsDir() {
			return nil, fmt.Errorf("simulator: identity store: parent path %q is not a directory", dir)
		}
	}

	lock, err := acquireInstanceLock(path + ".lock")
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", identityDSN(path))
	if err != nil {
		lock.release()
		return nil, fmt.Errorf("simulator: identity store: open: %w", err)
	}
	// This is a single logical writer (serialized by mu plus the instance
	// lock across processes); restrict to one connection so SQLite never
	// has to arbitrate between concurrent connections from within this
	// same process.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		lock.release()
		return nil, fmt.Errorf("simulator: identity store: ping: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sequence_counters (
		simulator_id TEXT NOT NULL,
		utc_date TEXT NOT NULL,
		next_sequence INTEGER NOT NULL,
		PRIMARY KEY (simulator_id, utc_date)
	)`); err != nil {
		db.Close()
		lock.release()
		return nil, fmt.Errorf("simulator: identity store: migrate: %w", err)
	}

	return &IdentityStore{db: db, lock: lock}, nil
}

// Close releases the instance lock and closes the underlying database.
func (s *IdentityStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Close()
	if lockErr := s.lock.release(); lockErr != nil && err == nil {
		err = lockErr
	}
	return err
}

// ReserveNextSequence durably reserves and returns the next sequence number
// for (simulatorID, utcDate), where utcDate is in "YYYYMMDD" format. The
// first call for a new (simulatorID, utcDate) pair returns 1. The
// reservation is committed to disk before this function returns
// successfully. Never returns a previously-issued sequence number, even
// across restarts (gaps after a crash between reservation and use are
// acceptable; reuse is never acceptable).
func (s *IdentityStore) ReserveNextSequence(simulatorID string, utcDate string) (int64, error) {
	if simulatorID == "" {
		return 0, fmt.Errorf("simulator: identity store: simulatorID must not be empty")
	}
	if !isValidUTCDate(utcDate) {
		return 0, fmt.Errorf("simulator: identity store: utcDate %q must be in YYYYMMDD format", utcDate)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("simulator: identity store: begin tx: %w", err)
	}
	defer tx.Rollback()

	var next int64
	err = tx.QueryRow(`SELECT next_sequence FROM sequence_counters WHERE simulator_id = ? AND utc_date = ?`, simulatorID, utcDate).Scan(&next)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		next = 1
	case err != nil:
		return 0, fmt.Errorf("simulator: identity store: read counter: %w", err)
	}

	if next == math.MaxInt64 {
		return 0, fmt.Errorf("simulator: identity store: sequence counter exhausted for simulator_id=%q utc_date=%q", simulatorID, utcDate)
	}
	reserved := next
	nextToStore := next + 1

	if _, err := tx.Exec(`INSERT INTO sequence_counters (simulator_id, utc_date, next_sequence) VALUES (?, ?, ?)
		ON CONFLICT (simulator_id, utc_date) DO UPDATE SET next_sequence = excluded.next_sequence`,
		simulatorID, utcDate, nextToStore); err != nil {
		return 0, fmt.Errorf("simulator: identity store: persist counter: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("simulator: identity store: commit: %w", err)
	}

	return reserved, nil
}

// isValidUTCDate reports whether s is exactly 8 ASCII digits (the
// "YYYYMMDD" format expected throughout this file). This is a cheap
// syntactic check only; it does not validate that the date is a real
// calendar date.
func isValidUTCDate(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// FormatPacketID formats a UTC date (YYYYMMDD) and sequence number into the
// canonical packet ID string "YYYYMMDD-SEQUENCE", zero-padded to at least 6
// digits (growing beyond 6 digits if sequence >= 1000000).
func FormatPacketID(utcDate string, sequence int64) string {
	return utcDate + "-" + fmt.Sprintf("%06d", sequence)
}

// UTCDateString formats t into the "YYYYMMDD" format expected by
// ReserveNextSequence/FormatPacketID, after converting it to UTC.
func UTCDateString(t time.Time) string {
	return t.UTC().Format("20060102")
}

// instanceLock implements the exclusive instance lock described on
// OpenIdentityStore's doc comment: a sibling lock file created with
// O_CREATE|O_EXCL, containing the current process's PID.
type instanceLock struct {
	path string
}

func acquireInstanceLock(lockPath string) (*instanceLock, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			holder := "unknown"
			if data, readErr := os.ReadFile(lockPath); readErr == nil {
				if pid := string(data); pid != "" {
					holder = pid
				}
			}
			return nil, fmt.Errorf("simulator: identity store: locked by another instance (pid %s, lock file %q); if that process is no longer running, delete the lock file manually to recover", holder, lockPath)
		}
		return nil, fmt.Errorf("simulator: identity store: create lock file %q: %w", lockPath, err)
	}
	defer f.Close()

	if _, err := f.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		os.Remove(lockPath)
		return nil, fmt.Errorf("simulator: identity store: write lock file %q: %w", lockPath, err)
	}

	return &instanceLock{path: lockPath}, nil
}

func (l *instanceLock) release() error {
	if l == nil {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("simulator: identity store: remove lock file %q: %w", l.path, err)
	}
	return nil
}
