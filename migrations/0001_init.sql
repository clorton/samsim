-- Initial schema for the GPS telemetry aggregator (AG-03).
-- Timestamps are stored as INTEGER (Unix epoch milliseconds). Bounds are
-- REAL. IDs are TEXT. No cascade deletes of history.

CREATE TABLE metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE vehicles (
    vehicle_id              TEXT PRIMARY KEY,
    latest_fix_timestamp_ms INTEGER,
    active_trip_id          TEXT,
    revision                INTEGER NOT NULL DEFAULT 0,
    updated_at_ms           INTEGER NOT NULL,
    FOREIGN KEY (vehicle_id, latest_fix_timestamp_ms)
        REFERENCES gps_fixes (vehicle_id, timestamp_ms),
    FOREIGN KEY (active_trip_id) REFERENCES trips (trip_id)
);

CREATE TABLE gps_fixes (
    vehicle_id         TEXT NOT NULL,
    timestamp_ms       INTEGER NOT NULL,
    latitude           REAL NOT NULL,
    longitude          REAL NOT NULL,
    speed_mps          REAL NOT NULL,
    heading_degrees    REAL NOT NULL,
    first_simulator_id TEXT NOT NULL,
    first_packet_id    TEXT NOT NULL,
    received_at_ms     INTEGER NOT NULL,
    assignment_reason  TEXT,
    PRIMARY KEY (vehicle_id, timestamp_ms),
    FOREIGN KEY (vehicle_id) REFERENCES vehicles (vehicle_id),
    CHECK (assignment_reason IS NULL OR assignment_reason = 'MANUAL_CLOSED_INTERVAL')
);

CREATE TABLE trips (
    trip_id             TEXT PRIMARY KEY,
    vehicle_id          TEXT NOT NULL,
    start_ms            INTEGER NOT NULL,
    end_ms              INTEGER NOT NULL,
    fix_count           INTEGER NOT NULL,
    min_latitude        REAL NOT NULL,
    max_latitude        REAL NOT NULL,
    min_longitude       REAL NOT NULL,
    max_longitude       REAL NOT NULL,
    status              TEXT NOT NULL,
    close_reason        TEXT NOT NULL,
    superseded_by_trip_id TEXT,
    created_revision    INTEGER NOT NULL,
    updated_revision    INTEGER NOT NULL,
    closed_at_ms        INTEGER,
    forced_close        INTEGER NOT NULL DEFAULT 0,
    missing_fix_count_at_close INTEGER,
    FOREIGN KEY (vehicle_id) REFERENCES vehicles (vehicle_id),
    FOREIGN KEY (superseded_by_trip_id) REFERENCES trips (trip_id),
    CHECK (start_ms <= end_ms),
    CHECK (fix_count > 0),
    CHECK (status IN ('ACTIVE', 'INACTIVE', 'CLOSED')),
    CHECK (close_reason IN ('NONE', 'MANUAL', 'MERGED')),
    CHECK (forced_close IN (0, 1)),
    CHECK (
        (status <> 'CLOSED' AND close_reason = 'NONE' AND superseded_by_trip_id IS NULL)
        OR (status = 'CLOSED' AND close_reason = 'MANUAL' AND superseded_by_trip_id IS NULL)
        OR (status = 'CLOSED' AND close_reason = 'MERGED' AND superseded_by_trip_id IS NOT NULL)
    )
);

-- At most one ACTIVE trip per vehicle.
CREATE UNIQUE INDEX idx_trips_one_active_per_vehicle
    ON trips (vehicle_id)
    WHERE status = 'ACTIVE';

CREATE INDEX idx_trips_vehicle_status_bounds
    ON trips (vehicle_id, status, start_ms, end_ms);

CREATE TABLE trip_fixes (
    trip_id      TEXT NOT NULL,
    vehicle_id   TEXT NOT NULL,
    timestamp_ms INTEGER NOT NULL,
    PRIMARY KEY (trip_id, timestamp_ms),
    FOREIGN KEY (trip_id) REFERENCES trips (trip_id),
    FOREIGN KEY (vehicle_id, timestamp_ms) REFERENCES gps_fixes (vehicle_id, timestamp_ms)
);

CREATE INDEX idx_trip_fixes_vehicle_timestamp
    ON trip_fixes (vehicle_id, timestamp_ms);

CREATE TABLE trip_merges (
    predecessor_trip_id TEXT PRIMARY KEY,
    successor_trip_id   TEXT NOT NULL,
    vehicle_id          TEXT NOT NULL,
    revision            INTEGER NOT NULL,
    merged_at_ms        INTEGER NOT NULL,
    FOREIGN KEY (predecessor_trip_id) REFERENCES trips (trip_id),
    FOREIGN KEY (successor_trip_id) REFERENCES trips (trip_id)
);

CREATE INDEX idx_trip_merges_successor
    ON trip_merges (successor_trip_id);

CREATE TABLE manual_barriers (
    trip_id         TEXT PRIMARY KEY,
    vehicle_id      TEXT NOT NULL,
    start_ms        INTEGER NOT NULL,
    end_ms          INTEGER NOT NULL,
    closed_revision INTEGER NOT NULL,
    FOREIGN KEY (trip_id) REFERENCES trips (trip_id),
    CHECK (start_ms <= end_ms)
);

CREATE INDEX idx_manual_barriers_vehicle
    ON manual_barriers (vehicle_id, start_ms, end_ms);

CREATE TABLE packets (
    simulator_id      TEXT NOT NULL,
    packet_id         TEXT NOT NULL,
    vehicle_id        TEXT NOT NULL,
    canonical_sha256  TEXT NOT NULL,
    received_at_ms    INTEGER NOT NULL,
    accepted_revision INTEGER NOT NULL,
    stored_result_json TEXT NOT NULL,
    PRIMARY KEY (simulator_id, packet_id),
    FOREIGN KEY (vehicle_id) REFERENCES vehicles (vehicle_id)
);

CREATE INDEX idx_packets_vehicle
    ON packets (vehicle_id);
