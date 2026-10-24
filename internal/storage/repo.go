package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrNotFound is returned by single-row lookups that find nothing.
var ErrNotFound = errors.New("storage: not found")

// GetPacket looks up a packet by its (simulator_id, packet_id) key, globally
// (not scoped by vehicle), per AG-04 step 3.
func GetPacket(ctx context.Context, tx *sql.Tx, simulatorID, packetID string) (*PacketRow, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT simulator_id, packet_id, vehicle_id, canonical_sha256, received_at_ms, accepted_revision, stored_result_json
		FROM packets WHERE simulator_id = ? AND packet_id = ?`, simulatorID, packetID)
	var p PacketRow
	if err := row.Scan(&p.SimulatorID, &p.PacketID, &p.VehicleID, &p.CanonicalSHA256, &p.ReceivedAtMs, &p.AcceptedRevision, &p.StoredResultJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: get packet: %w", err)
	}
	return &p, nil
}

// InsertPacket records a packet receipt.
func InsertPacket(ctx context.Context, tx *sql.Tx, p PacketRow) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO packets (simulator_id, packet_id, vehicle_id, canonical_sha256, received_at_ms, accepted_revision, stored_result_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.SimulatorID, p.PacketID, p.VehicleID, p.CanonicalSHA256, p.ReceivedAtMs, p.AcceptedRevision, p.StoredResultJSON)
	if err != nil {
		return fmt.Errorf("storage: insert packet: %w", err)
	}
	return nil
}

// FingerprintHex encodes a 32-byte SHA-256 fingerprint as lowercase hex for
// storage/comparison.
func FingerprintHex(fp [32]byte) string {
	return hex.EncodeToString(fp[:])
}

// GetVehicle looks up a vehicle by ID.
func GetVehicle(ctx context.Context, tx *sql.Tx, vehicleID string) (*VehicleRow, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT vehicle_id, latest_fix_timestamp_ms, active_trip_id, revision, updated_at_ms
		FROM vehicles WHERE vehicle_id = ?`, vehicleID)
	var v VehicleRow
	if err := row.Scan(&v.VehicleID, &v.LatestFixTimestampMs, &v.ActiveTripID, &v.Revision, &v.UpdatedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: get vehicle: %w", err)
	}
	return &v, nil
}

// EnsureVehicle inserts a vehicle row with revision 0 if it does not already
// exist, returning the current (possibly newly created) row.
func EnsureVehicle(ctx context.Context, tx *sql.Tx, vehicleID string, nowMs int64) (*VehicleRow, error) {
	v, err := GetVehicle(ctx, tx, vehicleID)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO vehicles (vehicle_id, latest_fix_timestamp_ms, active_trip_id, revision, updated_at_ms)
		VALUES (?, NULL, NULL, 0, ?)`, vehicleID, nowMs)
	if err != nil {
		return nil, fmt.Errorf("storage: ensure vehicle: %w", err)
	}
	return &VehicleRow{VehicleID: vehicleID, Revision: 0, UpdatedAtMs: nowMs}, nil
}

// UpdateVehicle persists the vehicle's latest fix pointer, active trip
// pointer, revision and updated_at_ms.
func UpdateVehicle(ctx context.Context, tx *sql.Tx, v VehicleRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE vehicles SET latest_fix_timestamp_ms = ?, active_trip_id = ?, revision = ?, updated_at_ms = ?
		WHERE vehicle_id = ?`,
		v.LatestFixTimestampMs, v.ActiveTripID, v.Revision, v.UpdatedAtMs, v.VehicleID)
	if err != nil {
		return fmt.Errorf("storage: update vehicle: %w", err)
	}
	return nil
}

// ListVehicles returns all vehicles ordered by vehicle_id ascending.
func ListVehicles(ctx context.Context, tx *sql.Tx) ([]VehicleRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT vehicle_id, latest_fix_timestamp_ms, active_trip_id, revision, updated_at_ms
		FROM vehicles ORDER BY vehicle_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list vehicles: %w", err)
	}
	defer rows.Close()
	var out []VehicleRow
	for rows.Next() {
		var v VehicleRow
		if err := rows.Scan(&v.VehicleID, &v.LatestFixTimestampMs, &v.ActiveTripID, &v.Revision, &v.UpdatedAtMs); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListVehiclesAfter returns up to limit vehicles with vehicle_id > afterID
// (empty string means from the start), ordered ascending by vehicle_id, for
// cursor pagination of the global vehicle list.
func ListVehiclesAfter(ctx context.Context, tx *sql.Tx, afterID string, limit int) ([]VehicleRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT vehicle_id, latest_fix_timestamp_ms, active_trip_id, revision, updated_at_ms
		FROM vehicles WHERE vehicle_id > ? ORDER BY vehicle_id ASC LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: list vehicles after: %w", err)
	}
	defer rows.Close()
	var out []VehicleRow
	for rows.Next() {
		var v VehicleRow
		if err := rows.Scan(&v.VehicleID, &v.LatestFixTimestampMs, &v.ActiveTripID, &v.Revision, &v.UpdatedAtMs); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetFix looks up one raw fix by vehicle/timestamp.
func GetFix(ctx context.Context, tx *sql.Tx, vehicleID string, timestampMs int64) (*FixRow, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT vehicle_id, timestamp_ms, latitude, longitude, speed_mps, heading_degrees,
		       first_simulator_id, first_packet_id, received_at_ms, assignment_reason
		FROM gps_fixes WHERE vehicle_id = ? AND timestamp_ms = ?`, vehicleID, timestampMs)
	var f FixRow
	if err := row.Scan(&f.VehicleID, &f.TimestampMs, &f.Latitude, &f.Longitude, &f.SpeedMps, &f.HeadingDegrees,
		&f.FirstSimulatorID, &f.FirstPacketID, &f.ReceivedAtMs, &f.AssignmentReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: get fix: %w", err)
	}
	return &f, nil
}

// GetFixesByTimestamps looks up multiple fixes for one vehicle by timestamp,
// returning a map keyed by timestamp for only the ones that exist.
func GetFixesByTimestamps(ctx context.Context, tx *sql.Tx, vehicleID string, timestamps []int64) (map[int64]FixRow, error) {
	out := make(map[int64]FixRow, len(timestamps))
	if len(timestamps) == 0 {
		return out, nil
	}
	// SQLite parameter limits are generous (default 32766); the weekend
	// scale here never approaches it (max_fixes_per_packet default 10000).
	query := "SELECT vehicle_id, timestamp_ms, latitude, longitude, speed_mps, heading_degrees, first_simulator_id, first_packet_id, received_at_ms, assignment_reason FROM gps_fixes WHERE vehicle_id = ? AND timestamp_ms IN ("
	args := make([]any, 0, len(timestamps)+1)
	args = append(args, vehicleID)
	for i, ts := range timestamps {
		if i > 0 {
			query += ","
		}
		query += "?"
		args = append(args, ts)
	}
	query += ")"
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: get fixes by timestamps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var f FixRow
		if err := rows.Scan(&f.VehicleID, &f.TimestampMs, &f.Latitude, &f.Longitude, &f.SpeedMps, &f.HeadingDegrees,
			&f.FirstSimulatorID, &f.FirstPacketID, &f.ReceivedAtMs, &f.AssignmentReason); err != nil {
			return nil, err
		}
		out[f.TimestampMs] = f
	}
	return out, rows.Err()
}

// InsertFix inserts a new raw fix row.
func InsertFix(ctx context.Context, tx *sql.Tx, f FixRow) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO gps_fixes (vehicle_id, timestamp_ms, latitude, longitude, speed_mps, heading_degrees,
		                       first_simulator_id, first_packet_id, received_at_ms, assignment_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.VehicleID, f.TimestampMs, f.Latitude, f.Longitude, f.SpeedMps, f.HeadingDegrees,
		f.FirstSimulatorID, f.FirstPacketID, f.ReceivedAtMs, f.AssignmentReason)
	if err != nil {
		return fmt.Errorf("storage: insert fix: %w", err)
	}
	return nil
}

// ListFixesByVehicle returns all raw fixes for a vehicle, chronological,
// optionally filtered by inclusive [fromMs,toMs] when non-nil, and by
// assignment ("assigned", "quarantined", or "" for all, resolved by the
// caller joining against trip_fixes since assignment is derived).
func ListFixesByVehicle(ctx context.Context, tx *sql.Tx, vehicleID string, fromMs, toMs *int64) ([]FixRow, error) {
	query := `SELECT vehicle_id, timestamp_ms, latitude, longitude, speed_mps, heading_degrees,
	                 first_simulator_id, first_packet_id, received_at_ms, assignment_reason
	          FROM gps_fixes WHERE vehicle_id = ?`
	args := []any{vehicleID}
	if fromMs != nil {
		query += " AND timestamp_ms >= ?"
		args = append(args, *fromMs)
	}
	if toMs != nil {
		query += " AND timestamp_ms <= ?"
		args = append(args, *toMs)
	}
	query += " ORDER BY timestamp_ms ASC"
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list fixes: %w", err)
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

// ListAllTimestampsByVehicle returns every raw fix timestamp for a vehicle,
// ascending, regardless of assignment.
func ListAllTimestampsByVehicle(ctx context.Context, tx *sql.Tx, vehicleID string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT timestamp_ms FROM gps_fixes WHERE vehicle_id = ? ORDER BY timestamp_ms ASC`, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("storage: list timestamps: %w", err)
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

// MaxFixTimestamp returns the maximum raw fix timestamp for a vehicle, or
// (0, false) if the vehicle has no fixes.
func MaxFixTimestamp(ctx context.Context, tx *sql.Tx, vehicleID string) (int64, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT MAX(timestamp_ms) FROM gps_fixes WHERE vehicle_id = ?`, vehicleID)
	var max sql.NullInt64
	if err := row.Scan(&max); err != nil {
		return 0, false, fmt.Errorf("storage: max fix timestamp: %w", err)
	}
	if !max.Valid {
		return 0, false, nil
	}
	return max.Int64, true, nil
}

// GetCurrentTripIDForFix returns the trip_id currently assigned to a fix via
// trip_fixes joined to a non-MERGED trip, or ErrNotFound if unassigned
// (quarantined or not yet joined).
func GetCurrentTripIDForFix(ctx context.Context, tx *sql.Tx, vehicleID string, timestampMs int64) (string, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT tf.trip_id FROM trip_fixes tf
		JOIN trips t ON t.trip_id = tf.trip_id
		WHERE tf.vehicle_id = ? AND tf.timestamp_ms = ? AND t.close_reason <> 'MERGED'`, vehicleID, timestampMs)
	var tripID string
	if err := row.Scan(&tripID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("storage: get current trip for fix: %w", err)
	}
	return tripID, nil
}

// FixWithCurrentTrip pairs a raw fix with its current (non-MERGED) trip
// assignment, if any.
type FixWithCurrentTrip struct {
	Fix           FixRow
	CurrentTripID *string
}

// ListFixesWithCurrentTripByVehicle returns raw fixes for a vehicle,
// chronological, each annotated with its current trip assignment (nil if
// unassigned/quarantined), optionally filtered by inclusive [fromMs,toMs]
// and a minimum exclusive timestamp (afterMs, for cursor pagination; nil
// means from the start). Results are limited to at most limit rows.
func ListFixesWithCurrentTripByVehicle(ctx context.Context, tx *sql.Tx, vehicleID string, fromMs, toMs, afterMs *int64, limit int) ([]FixWithCurrentTrip, error) {
	query := `
		SELECT f.vehicle_id, f.timestamp_ms, f.latitude, f.longitude, f.speed_mps, f.heading_degrees,
		       f.first_simulator_id, f.first_packet_id, f.received_at_ms, f.assignment_reason,
		       (SELECT tf.trip_id FROM trip_fixes tf
		        JOIN trips t ON t.trip_id = tf.trip_id
		        WHERE tf.vehicle_id = f.vehicle_id AND tf.timestamp_ms = f.timestamp_ms AND t.close_reason <> 'MERGED'
		        LIMIT 1) AS current_trip_id
		FROM gps_fixes f
		WHERE f.vehicle_id = ?`
	args := []any{vehicleID}
	if fromMs != nil {
		query += " AND f.timestamp_ms >= ?"
		args = append(args, *fromMs)
	}
	if toMs != nil {
		query += " AND f.timestamp_ms <= ?"
		args = append(args, *toMs)
	}
	if afterMs != nil {
		query += " AND f.timestamp_ms > ?"
		args = append(args, *afterMs)
	}
	query += " ORDER BY f.timestamp_ms ASC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list fixes with current trip: %w", err)
	}
	defer rows.Close()
	var out []FixWithCurrentTrip
	for rows.Next() {
		var f FixRow
		var currentTripID sql.NullString
		if err := rows.Scan(&f.VehicleID, &f.TimestampMs, &f.Latitude, &f.Longitude, &f.SpeedMps, &f.HeadingDegrees,
			&f.FirstSimulatorID, &f.FirstPacketID, &f.ReceivedAtMs, &f.AssignmentReason, &currentTripID); err != nil {
			return nil, err
		}
		item := FixWithCurrentTrip{Fix: f}
		if currentTripID.Valid {
			id := currentTripID.String
			item.CurrentTripID = &id
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
