package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetTrip looks up one trip by ID.
func GetTrip(ctx context.Context, tx *sql.Tx, tripID string) (*TripRow, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT trip_id, vehicle_id, start_ms, end_ms, fix_count, min_latitude, max_latitude,
		       min_longitude, max_longitude, status, close_reason, superseded_by_trip_id,
		       created_revision, updated_revision, closed_at_ms, forced_close, missing_fix_count_at_close
		FROM trips WHERE trip_id = ?`, tripID)
	t, err := scanTrip(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: get trip: %w", err)
	}
	return t, nil
}

func scanTrip(row *sql.Row) (*TripRow, error) {
	var t TripRow
	var forcedClose int
	if err := row.Scan(&t.TripID, &t.VehicleID, &t.StartMs, &t.EndMs, &t.FixCount, &t.MinLatitude, &t.MaxLatitude,
		&t.MinLongitude, &t.MaxLongitude, &t.Status, &t.CloseReason, &t.SupersededByTripID,
		&t.CreatedRevision, &t.UpdatedRevision, &t.ClosedAtMs, &forcedClose, &t.MissingFixCountAtClose); err != nil {
		return nil, err
	}
	t.ForcedClose = forcedClose != 0
	return &t, nil
}

// ListMutableTripsByVehicle returns all ACTIVE/INACTIVE trips for a vehicle.
func ListMutableTripsByVehicle(ctx context.Context, tx *sql.Tx, vehicleID string) ([]TripRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT trip_id, vehicle_id, start_ms, end_ms, fix_count, min_latitude, max_latitude,
		       min_longitude, max_longitude, status, close_reason, superseded_by_trip_id,
		       created_revision, updated_revision, closed_at_ms, forced_close, missing_fix_count_at_close
		FROM trips WHERE vehicle_id = ? AND status IN ('ACTIVE','INACTIVE')`, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("storage: list mutable trips: %w", err)
	}
	defer rows.Close()
	return scanTripRows(rows)
}

// ListCurrentTripsByVehicle returns ACTIVE, INACTIVE and CLOSED/MANUAL trips
// (i.e. excludes CLOSED/MERGED predecessors unless includeMerged), optionally
// filtered by status.
func ListCurrentTripsByVehicle(ctx context.Context, tx *sql.Tx, vehicleID string, includeMerged bool, statusFilter string) ([]TripRow, error) {
	query := `
		SELECT trip_id, vehicle_id, start_ms, end_ms, fix_count, min_latitude, max_latitude,
		       min_longitude, max_longitude, status, close_reason, superseded_by_trip_id,
		       created_revision, updated_revision, closed_at_ms, forced_close, missing_fix_count_at_close
		FROM trips WHERE vehicle_id = ?`
	args := []any{vehicleID}
	if !includeMerged {
		query += " AND close_reason <> 'MERGED'"
	}
	if statusFilter != "" {
		query += " AND status = ?"
		args = append(args, statusFilter)
	}
	query += " ORDER BY start_ms ASC, trip_id ASC"
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list current trips: %w", err)
	}
	defer rows.Close()
	return scanTripRows(rows)
}

// ListCurrentTripsAfter is ListCurrentTripsByVehicle with cursor pagination:
// it returns at most limit trips strictly after (afterStartMs, afterTripID)
// in (start_ms, trip_id) order.
func ListCurrentTripsAfter(ctx context.Context, tx *sql.Tx, vehicleID string, includeMerged bool, statusFilter string, afterStartMs *int64, afterTripID string, limit int) ([]TripRow, error) {
	query := `
		SELECT trip_id, vehicle_id, start_ms, end_ms, fix_count, min_latitude, max_latitude,
		       min_longitude, max_longitude, status, close_reason, superseded_by_trip_id,
		       created_revision, updated_revision, closed_at_ms, forced_close, missing_fix_count_at_close
		FROM trips WHERE vehicle_id = ?`
	args := []any{vehicleID}
	if !includeMerged {
		query += " AND close_reason <> 'MERGED'"
	}
	if statusFilter != "" {
		query += " AND status = ?"
		args = append(args, statusFilter)
	}
	if afterStartMs != nil {
		query += " AND (start_ms > ? OR (start_ms = ? AND trip_id > ?))"
		args = append(args, *afterStartMs, *afterStartMs, afterTripID)
	}
	query += " ORDER BY start_ms ASC, trip_id ASC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list current trips after: %w", err)
	}
	defer rows.Close()
	return scanTripRows(rows)
}

func scanTripRows(rows *sql.Rows) ([]TripRow, error) {
	var out []TripRow
	for rows.Next() {
		var t TripRow
		var forcedClose int
		if err := rows.Scan(&t.TripID, &t.VehicleID, &t.StartMs, &t.EndMs, &t.FixCount, &t.MinLatitude, &t.MaxLatitude,
			&t.MinLongitude, &t.MaxLongitude, &t.Status, &t.CloseReason, &t.SupersededByTripID,
			&t.CreatedRevision, &t.UpdatedRevision, &t.ClosedAtMs, &forcedClose, &t.MissingFixCountAtClose); err != nil {
			return nil, err
		}
		t.ForcedClose = forcedClose != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// InsertTrip inserts a new trip row.
func InsertTrip(ctx context.Context, tx *sql.Tx, t TripRow) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO trips (trip_id, vehicle_id, start_ms, end_ms, fix_count, min_latitude, max_latitude,
		                   min_longitude, max_longitude, status, close_reason, superseded_by_trip_id,
		                   created_revision, updated_revision, closed_at_ms, forced_close, missing_fix_count_at_close)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TripID, t.VehicleID, t.StartMs, t.EndMs, t.FixCount, t.MinLatitude, t.MaxLatitude,
		t.MinLongitude, t.MaxLongitude, t.Status, t.CloseReason, t.SupersededByTripID,
		t.CreatedRevision, t.UpdatedRevision, t.ClosedAtMs, boolToInt(t.ForcedClose), t.MissingFixCountAtClose)
	if err != nil {
		return fmt.Errorf("storage: insert trip: %w", err)
	}
	return nil
}

// UpdateTrip updates all mutable fields of an existing trip row (by
// trip_id).
func UpdateTrip(ctx context.Context, tx *sql.Tx, t TripRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE trips SET start_ms = ?, end_ms = ?, fix_count = ?, min_latitude = ?, max_latitude = ?,
		       min_longitude = ?, max_longitude = ?, status = ?, close_reason = ?, superseded_by_trip_id = ?,
		       updated_revision = ?, closed_at_ms = ?, forced_close = ?, missing_fix_count_at_close = ?
		WHERE trip_id = ?`,
		t.StartMs, t.EndMs, t.FixCount, t.MinLatitude, t.MaxLatitude, t.MinLongitude, t.MaxLongitude,
		t.Status, t.CloseReason, t.SupersededByTripID, t.UpdatedRevision, t.ClosedAtMs,
		boolToInt(t.ForcedClose), t.MissingFixCountAtClose, t.TripID)
	if err != nil {
		return fmt.Errorf("storage: update trip: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// InsertTripFix adds one fix to a trip's membership.
func InsertTripFix(ctx context.Context, tx *sql.Tx, tripID, vehicleID string, timestampMs int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO trip_fixes (trip_id, vehicle_id, timestamp_ms) VALUES (?, ?, ?)`,
		tripID, vehicleID, timestampMs)
	if err != nil {
		return fmt.Errorf("storage: insert trip fix: %w", err)
	}
	return nil
}

// ListTripFixTimestamps returns all member timestamps of a trip, ascending.
func ListTripFixTimestamps(ctx context.Context, tx *sql.Tx, tripID string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT timestamp_ms FROM trip_fixes WHERE trip_id = ? ORDER BY timestamp_ms ASC`, tripID)
	if err != nil {
		return nil, fmt.Errorf("storage: list trip fix timestamps: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var ts int64
		if err := rows.Scan(&ts); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// ListTripFixes returns all member raw fixes of a trip, ascending by
// timestamp (joins trip_fixes to gps_fixes).
func ListTripFixes(ctx context.Context, tx *sql.Tx, tripID string) ([]FixRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT f.vehicle_id, f.timestamp_ms, f.latitude, f.longitude, f.speed_mps, f.heading_degrees,
		       f.first_simulator_id, f.first_packet_id, f.received_at_ms, f.assignment_reason
		FROM trip_fixes tf
		JOIN gps_fixes f ON f.vehicle_id = tf.vehicle_id AND f.timestamp_ms = tf.timestamp_ms
		WHERE tf.trip_id = ?
		ORDER BY f.timestamp_ms ASC`, tripID)
	if err != nil {
		return nil, fmt.Errorf("storage: list trip fixes: %w", err)
	}
	defer rows.Close()
	var out []FixRow
	for rows.Next() {
		var f FixRow
		if err := rows.Scan(&f.VehicleID, &f.TimestampMs, &f.Latitude, &f.Longitude, &f.SpeedMps, &f.HeadingDegrees,
			&f.FirstSimulatorID, &f.FirstPacketID, &f.ReceivedAtMs, &f.AssignmentReason); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// InsertTripMerge records a predecessor -> successor lineage edge.
func InsertTripMerge(ctx context.Context, tx *sql.Tx, m TripMergeRow) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO trip_merges (predecessor_trip_id, successor_trip_id, vehicle_id, revision, merged_at_ms)
		VALUES (?, ?, ?, ?, ?)`, m.PredecessorTripID, m.SuccessorTripID, m.VehicleID, m.Revision, m.MergedAtMs)
	if err != nil {
		return fmt.Errorf("storage: insert trip merge: %w", err)
	}
	return nil
}

// GetImmediateSuccessor returns the immediate successor trip ID for a
// predecessor, or ErrNotFound if it has none.
func GetImmediateSuccessor(ctx context.Context, tx *sql.Tx, predecessorTripID string) (string, error) {
	row := tx.QueryRowContext(ctx, `SELECT successor_trip_id FROM trip_merges WHERE predecessor_trip_id = ?`, predecessorTripID)
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("storage: get immediate successor: %w", err)
	}
	return id, nil
}

// ResolveTerminalSuccessor follows the lineage chain to the final
// non-superseded trip ID, detecting cycles defensively.
func ResolveTerminalSuccessor(ctx context.Context, tx *sql.Tx, tripID string) (string, error) {
	seen := map[string]bool{tripID: true}
	current := tripID
	for {
		next, err := GetImmediateSuccessor(ctx, tx, current)
		if errors.Is(err, ErrNotFound) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if seen[next] {
			return "", fmt.Errorf("storage: lineage cycle detected at trip %q", next)
		}
		seen[next] = true
		current = next
	}
}

// ListImmediatePredecessors returns all immediate predecessor trip IDs of
// successorTripID.
func ListImmediatePredecessors(ctx context.Context, tx *sql.Tx, successorTripID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT predecessor_trip_id FROM trip_merges WHERE successor_trip_id = ?`, successorTripID)
	if err != nil {
		return nil, fmt.Errorf("storage: list immediate predecessors: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// InsertManualBarrier records a new immutable closed interval.
func InsertManualBarrier(ctx context.Context, tx *sql.Tx, b ManualBarrierRow) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO manual_barriers (trip_id, vehicle_id, start_ms, end_ms, closed_revision)
		VALUES (?, ?, ?, ?, ?)`, b.TripID, b.VehicleID, b.StartMs, b.EndMs, b.ClosedRevision)
	if err != nil {
		return fmt.Errorf("storage: insert manual barrier: %w", err)
	}
	return nil
}

// ListManualBarriersByVehicle returns all manual barriers for a vehicle,
// ordered ascending by start_ms.
func ListManualBarriersByVehicle(ctx context.Context, tx *sql.Tx, vehicleID string) ([]ManualBarrierRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT trip_id, vehicle_id, start_ms, end_ms, closed_revision
		FROM manual_barriers WHERE vehicle_id = ? ORDER BY start_ms ASC`, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("storage: list manual barriers: %w", err)
	}
	defer rows.Close()
	var out []ManualBarrierRow
	for rows.Next() {
		var b ManualBarrierRow
		if err := rows.Scan(&b.TripID, &b.VehicleID, &b.StartMs, &b.EndMs, &b.ClosedRevision); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
