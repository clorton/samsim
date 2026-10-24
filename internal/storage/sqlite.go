// Package storage implements SQLite persistence for the aggregator: ordered
// migrations, connection setup (WAL, foreign keys, busy timeout), a
// process-wide writer coordinator, and repository helpers (AG-03).
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"gpsdemo/migrations"
)

// nowMs returns the current wall-clock time in Unix epoch milliseconds, used
// only for migration bookkeeping (not domain-level timestamps, which are
// always sourced from the injected clock).
func nowMs() int64 {
	return time.Now().UnixMilli()
}

// Metadata keys stored in the metadata table.
const (
	metaDatabaseID            = "database_id"
	metaTripGapSeconds        = "trip_gap_seconds"
	metaExpectedFixIntervalMs = "expected_fix_interval_ms"
)

// Store owns the aggregator's SQLite connections: a single-connection write
// pool funneled through a process-wide writer coordinator, and a multi-
// connection read pool for concurrent coherent reads.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB

	writerMu sync.Mutex

	// precommitFailureHook is a test-only injection point; see
	// SetPrecommitFailureHook.
	precommitFailureHook func() error

	DatabaseID            string
	TripGapSeconds        int64
	ExpectedFixIntervalMs int64
}

// Options configures Store creation.
type Options struct {
	Path                  string
	BusyTimeoutMs         int64
	TripGapSeconds        int64
	ExpectedFixIntervalMs int64
}

func dsn(path string, busyTimeoutMs int64, extra map[string]string) string {
	v := url.Values{}
	v.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMs))
	v.Add("_pragma", "foreign_keys(1)")
	v.Add("_pragma", "journal_mode(WAL)")
	v.Add("_pragma", "synchronous(FULL)")
	for k, val := range extra {
		v.Add(k, val)
	}
	return "file:" + path + "?" + v.Encode()
}

// Open opens (creating if necessary) the SQLite database at opts.Path,
// applies migrations, validates/persists frozen metadata, and returns a
// ready Store.
func Open(ctx context.Context, opts Options) (*Store, error) {
	writeDB, err := sql.Open("sqlite", dsn(opts.Path, opts.BusyTimeoutMs, nil))
	if err != nil {
		return nil, fmt.Errorf("storage: open write connection: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)

	readDB, err := sql.Open("sqlite", dsn(opts.Path, opts.BusyTimeoutMs, map[string]string{"_query_only": "1"}))
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("storage: open read connection: %w", err)
	}
	readDB.SetMaxOpenConns(4)

	s := &Store{writeDB: writeDB, readDB: readDB}

	if err := s.migrate(ctx); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, err
	}

	if err := s.initOrValidateMetadata(ctx, opts); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, err
	}

	return s, nil
}

// Close closes both connection pools.
func (s *Store) Close() error {
	err1 := s.writeDB.Close()
	err2 := s.readDB.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// Ping verifies the write connection is usable, for health checks.
func (s *Store) Ping(ctx context.Context) error {
	return s.writeDB.PingContext(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	conn, err := s.writeDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("storage: migrate: acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at_ms INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("storage: migrate: create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("storage: migrate: read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("storage: migrate: scan version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("storage: migrate: read embedded migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for i, name := range names {
		version := i + 1
		if applied[version] {
			continue
		}
		data, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("storage: migrate: read %s: %w", name, err)
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("storage: migrate: begin %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(data)); err != nil {
			tx.Rollback()
			return fmt.Errorf("storage: migrate: apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at_ms) VALUES (?, ?)`, version, nowMs()); err != nil {
			tx.Rollback()
			return fmt.Errorf("storage: migrate: record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("storage: migrate: commit %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) initOrValidateMetadata(ctx context.Context, opts Options) error {
	conn, err := s.writeDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	existing, err := readAllMetadata(ctx, tx)
	if err != nil {
		return err
	}

	if len(existing) == 0 {
		dbID := uuid.NewString()
		if err := writeMetadata(ctx, tx, map[string]string{
			metaDatabaseID:            dbID,
			metaTripGapSeconds:        fmt.Sprintf("%d", opts.TripGapSeconds),
			metaExpectedFixIntervalMs: fmt.Sprintf("%d", opts.ExpectedFixIntervalMs),
		}); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.DatabaseID = dbID
		s.TripGapSeconds = opts.TripGapSeconds
		s.ExpectedFixIntervalMs = opts.ExpectedFixIntervalMs
		return nil
	}

	// Existing database: values are frozen. Reject a differing config rather
	// than silently reinterpreting frozen history.
	if err := tx.Commit(); err != nil {
		return err
	}
	dbID, ok := existing[metaDatabaseID]
	if !ok {
		return fmt.Errorf("storage: metadata missing %s", metaDatabaseID)
	}
	gapStr, ok := existing[metaTripGapSeconds]
	if !ok {
		return fmt.Errorf("storage: metadata missing %s", metaTripGapSeconds)
	}
	intervalStr, ok := existing[metaExpectedFixIntervalMs]
	if !ok {
		return fmt.Errorf("storage: metadata missing %s", metaExpectedFixIntervalMs)
	}
	var gap, interval int64
	if _, err := fmt.Sscanf(gapStr, "%d", &gap); err != nil {
		return fmt.Errorf("storage: parse stored trip_gap_seconds: %w", err)
	}
	if _, err := fmt.Sscanf(intervalStr, "%d", &interval); err != nil {
		return fmt.Errorf("storage: parse stored expected_fix_interval_ms: %w", err)
	}
	if gap != opts.TripGapSeconds {
		return fmt.Errorf("storage: configured trip_gap_seconds=%d conflicts with frozen database value %d", opts.TripGapSeconds, gap)
	}
	if interval != opts.ExpectedFixIntervalMs {
		return fmt.Errorf("storage: configured expected_fix_interval_ms=%d conflicts with frozen database value %d", opts.ExpectedFixIntervalMs, interval)
	}
	s.DatabaseID = dbID
	s.TripGapSeconds = gap
	s.ExpectedFixIntervalMs = interval
	return nil
}

func readAllMetadata(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM metadata`)
	if err != nil {
		return nil, fmt.Errorf("storage: read metadata: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func writeMetadata(ctx context.Context, tx *sql.Tx, kv map[string]string) error {
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata (key, value) VALUES (?, ?)`, k, v); err != nil {
			return fmt.Errorf("storage: write metadata %s: %w", k, err)
		}
	}
	return nil
}

// WriteResult carries whatever a writer transaction function wants to pass
// out to the post-commit publish step.
type WriteResult[T any] struct {
	Value T
}

// WithWriteTx runs fn inside a single write transaction serialized by the
// process-wide writer coordinator. If fn returns an error, the transaction
// is rolled back and publish is never called. On success, the transaction is
// committed, and then publish (if non-nil) is invoked synchronously while
// the writer coordinator lock is still held, guaranteeing commit-order
// delivery. publish must not block on network I/O.
func WithWriteTx[T any](ctx context.Context, s *Store, fn func(ctx context.Context, tx *sql.Tx) (T, error), publish func(T)) (T, error) {
	var zero T
	s.writerMu.Lock()
	defer s.writerMu.Unlock()

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("storage: begin write tx: %w", err)
	}

	result, err := fn(ctx, tx)
	if err != nil {
		tx.Rollback()
		return zero, err
	}

	// Test-only precommit failure injection (AG-10/T14): if set, this runs
	// after fn has staged all writes in tx but before the commit actually
	// happens, letting tests verify that a failure here leaves no partial
	// packet/fix/trip/revision/event trace. Never set outside tests.
	if s.precommitFailureHook != nil {
		if hookErr := s.precommitFailureHook(); hookErr != nil {
			tx.Rollback()
			return zero, hookErr
		}
	}

	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("storage: commit write tx: %w", err)
	}
	if publish != nil {
		publish(result)
	}
	return result, nil
}

// SetPrecommitFailureHook installs or clears (pass nil) a test-only hook
// invoked inside every WithWriteTx call after the transaction function
// succeeds but before commit. Used by AG-10/T14 crash-injection tests to
// simulate a failure between "all writes staged" and "durably committed"
// without needing real process-crash infrastructure. Not safe to call
// concurrently with in-flight WithWriteTx calls; set it before the
// triggering operation and clear it immediately after.
func (s *Store) SetPrecommitFailureHook(hook func() error) {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	s.precommitFailureHook = hook
}

// WithWriterLock runs fn while holding the process-wide writer coordinator
// lock, without starting a transaction. This lets callers that need to
// observe current state and register for future commits atomically (e.g.
// AG-08's subscribe-then-ready handshake) do so without racing a concurrent
// commit: no WithWriteTx call can interleave while fn runs.
func (s *Store) WithWriterLock(fn func() error) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	return fn()
}

// ReadUnderWriterLock runs fn in a short read-only transaction on the write
// connection while the writer coordinator lock is held (see
// WithWriterLock). Using the write connection (not the read pool)
// guarantees fn observes every previously committed write with no
// replication lag, which matters because the caller is about to register
// for future commits and must not miss or double-count the one that just
// landed.
func ReadUnderWriterLock[T any](ctx context.Context, s *Store, fn func(ctx context.Context, tx *sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := s.writeDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, fmt.Errorf("storage: begin read-under-writer-lock tx: %w", err)
	}
	defer tx.Rollback()

	result, err := fn(ctx, tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("storage: commit read-under-writer-lock tx: %w", err)
	}
	return result, nil
}

// ReadTx runs fn inside a coherent read-only transaction on the read pool.
func ReadTx[T any](ctx context.Context, s *Store, fn func(ctx context.Context, tx *sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, fmt.Errorf("storage: begin read tx: %w", err)
	}
	defer tx.Rollback()

	result, err := fn(ctx, tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("storage: commit read tx: %w", err)
	}
	return result, nil
}
