// Package simulator implements the route-playback GPS simulator described
// in SIMULATOR_SPEC.md. This file implements SIM-01/SIM-02's CSV route
// loader: parsing, validating, and timestamp-resolving a complete route CSV
// file into an in-memory slice of RouteRecord values.
//
// This package has zero dependency on any aggregator-specific package
// (internal/apierr, internal/storage, internal/aggregator, wire code, etc).
// It only depends on the Go standard library.
package simulator

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"math/big"
	"strings"
)

// RouteRecord is one validated, timestamp-resolved fix from a loaded route CSV.
type RouteRecord struct {
	TimestampMs    int64
	Latitude       float64
	Longitude      float64
	SpeedMps       float32
	HeadingDegrees float32

	// Optional diagnostics; nil/empty if not present in the source CSV.
	DistanceM   *float64
	SpeedSource string
}

// LoadRouteCSVOptions bounds resource usage during parsing.
//
// MaxBytes and MaxRows of zero (or negative) are treated as "no limit";
// callers should pass the configured max_route_bytes/max_route_rows
// values to enforce bounds before unbounded memory allocation.
type LoadRouteCSVOptions struct {
	ResolvedRunStartMs int64
	MaxBytes           int64
	MaxRows            int64
}

// Required and optional CSV column names.
const (
	colTS          = "ts"
	colLat         = "lat"
	colLon         = "lon"
	colSpeedMps    = "speed_mps"
	colHeadingDeg  = "heading_deg"
	colDistM       = "dist_m"
	colSpeedSource = "speed_source"
)

// Maximum valid epoch-millisecond timestamp accepted by the aggregator:
// end of year 9999 (see AG-02).
const maxEpochMs int64 = 253402300799999

// utf8BOM is the single UTF-8 byte-order mark that may optionally prefix the
// file.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// LoadRouteCSV parses, validates, and timestamp-resolves a complete route
// CSV file per SIM-01/02. It returns a complete slice of records in
// ascending timestamp order, or a descriptive error referencing the
// offending record number and field. It never returns a partial result on
// error.
//
// Record numbers in error messages are 1-based counting from the first
// data row after the header (i.e. the header itself is not counted, and
// the row immediately following the header is record 1).
func LoadRouteCSV(data []byte, opts LoadRouteCSVOptions) ([]RouteRecord, error) {
	if opts.MaxBytes > 0 && int64(len(data)) > opts.MaxBytes {
		return nil, fmt.Errorf("route csv: input size %d bytes exceeds max_route_bytes limit %d", len(data), opts.MaxBytes)
	}

	data = bytes.TrimPrefix(data, utf8BOM)

	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1 // we validate width ourselves against the header
	reader.ReuseRecord = false

	header, err := reader.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("route csv: file is empty (no header row)")
	}
	if err != nil {
		return nil, fmt.Errorf("route csv: malformed header: %w", err)
	}

	idx, err := resolveHeaderIndex(header)
	if err != nil {
		return nil, err
	}

	var records []RouteRecord
	var prevTSRat *big.Rat
	var prevOffsetMs int64
	var prevDistM *float64
	recordNum := int64(0)

	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("route csv: malformed record at row %d: %w", recordNum+1, err)
		}

		recordNum++
		if opts.MaxRows > 0 && recordNum > opts.MaxRows {
			return nil, fmt.Errorf("route csv: row count exceeds max_route_rows limit %d", opts.MaxRows)
		}

		if len(row) != len(header) {
			return nil, fmt.Errorf("route csv: record %d: inconsistent record width: expected %d fields, got %d", recordNum, len(header), len(row))
		}

		rec, tsRat, offsetMs, err := parseRecord(row, idx, recordNum, opts.ResolvedRunStartMs, prevDistM)
		if err != nil {
			return nil, err
		}

		if prevTSRat != nil {
			if tsRat.Cmp(prevTSRat) <= 0 {
				return nil, fmt.Errorf("route csv: record %d: field %q: ts must be strictly increasing (previous %s, got %s)", recordNum, colTS, prevTSRat.FloatString(6), tsRat.FloatString(6))
			}
			if offsetMs <= prevOffsetMs {
				return nil, fmt.Errorf("route csv: record %d: field %q: rounded offset_ms collides with previous record's offset_ms (%d); both round to the same millisecond", recordNum, colTS, offsetMs)
			}
		}
		prevTSRat = tsRat
		prevOffsetMs = offsetMs
		prevDistM = rec.DistanceM

		records = append(records, rec)
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("route csv: empty route: no data rows found after header")
	}

	return records, nil
}

// headerIndex maps required/optional column names to their column position
// in the CSV header.
type headerIndex struct {
	ts          int
	lat         int
	lon         int
	speedMps    int
	headingDeg  int
	distM       int // -1 if absent
	speedSource int // -1 if absent
}

// resolveHeaderIndex validates the header row: the five required telemetry
// columns must each appear exactly once, no header name may be duplicated
// (including the optional/unknown ones), and extra unknown columns are
// permitted (ignored).
func resolveHeaderIndex(header []string) (headerIndex, error) {
	idx := headerIndex{ts: -1, lat: -1, lon: -1, speedMps: -1, headingDeg: -1, distM: -1, speedSource: -1}

	seen := make(map[string]int, len(header))
	for i, name := range header {
		if prevIdx, ok := seen[name]; ok {
			return idx, fmt.Errorf("route csv: duplicate header column %q at positions %d and %d", name, prevIdx, i)
		}
		seen[name] = i

		switch name {
		case colTS:
			idx.ts = i
		case colLat:
			idx.lat = i
		case colLon:
			idx.lon = i
		case colSpeedMps:
			idx.speedMps = i
		case colHeadingDeg:
			idx.headingDeg = i
		case colDistM:
			idx.distM = i
		case colSpeedSource:
			idx.speedSource = i
		}
	}

	var missing []string
	if idx.ts < 0 {
		missing = append(missing, colTS)
	}
	if idx.lat < 0 {
		missing = append(missing, colLat)
	}
	if idx.lon < 0 {
		missing = append(missing, colLon)
	}
	if idx.speedMps < 0 {
		missing = append(missing, colSpeedMps)
	}
	if idx.headingDeg < 0 {
		missing = append(missing, colHeadingDeg)
	}
	if len(missing) > 0 {
		return idx, fmt.Errorf("route csv: missing required header column(s): %s", strings.Join(missing, ", "))
	}

	return idx, nil
}

// parseRecord parses and validates a single CSV data row, returning the
// resolved RouteRecord along with the exact decimal ts value (as a
// big.Rat) and its rounded offset_ms, for strictly-increasing checks by
// the caller.
func parseRecord(row []string, idx headerIndex, recordNum int64, runStartMs int64, prevDistM *float64) (RouteRecord, *big.Rat, int64, error) {
	var rec RouteRecord

	tsStr := row[idx.ts]
	if tsStr == "" {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: value is empty", recordNum, colTS)
	}
	tsRat, ok := parseDecimalToRat(tsStr)
	if !ok {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: invalid decimal value %q", recordNum, colTS, tsStr)
	}
	if tsRat.Sign() < 0 {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: value %q must be nonnegative", recordNum, colTS, tsStr)
	}

	offsetMs, err := roundHalfUpMs(tsRat)
	if err != nil {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: %w", recordNum, colTS, err)
	}

	timestampMs, err := addTimestamp(runStartMs, offsetMs)
	if err != nil {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: %w", recordNum, colTS, err)
	}
	if timestampMs <= 0 || timestampMs > maxEpochMs {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: resolved timestamp_ms %d out of valid range (0, %d]", recordNum, colTS, timestampMs, maxEpochMs)
	}

	lat, err := parseRequiredFloat64(row[idx.lat], colLat, recordNum)
	if err != nil {
		return rec, nil, 0, err
	}
	if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: value %v out of range [-90,90]", recordNum, colLat, lat)
	}

	lon, err := parseRequiredFloat64(row[idx.lon], colLon, recordNum)
	if err != nil {
		return rec, nil, 0, err
	}
	if math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: value %v out of range [-180,180]", recordNum, colLon, lon)
	}

	speed, err := parseRequiredFloat64(row[idx.speedMps], colSpeedMps, recordNum)
	if err != nil {
		return rec, nil, 0, err
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < 0 {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: value %v must be >= 0", recordNum, colSpeedMps, speed)
	}

	heading, err := parseRequiredFloat64(row[idx.headingDeg], colHeadingDeg, recordNum)
	if err != nil {
		return rec, nil, 0, err
	}
	if math.IsNaN(heading) || math.IsInf(heading, 0) || heading < 0 || heading >= 360 {
		return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: value %v out of range [0,360)", recordNum, colHeadingDeg, heading)
	}

	rec.TimestampMs = timestampMs
	rec.Latitude = lat
	rec.Longitude = lon
	rec.SpeedMps = float32(speed)
	rec.HeadingDegrees = float32(heading)

	if idx.distM >= 0 {
		distStr := row[idx.distM]
		if distStr != "" {
			dist, err := parseFloat64Strict(distStr)
			if err != nil {
				return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: invalid numeric value %q", recordNum, colDistM, distStr)
			}
			if math.IsNaN(dist) || math.IsInf(dist, 0) {
				return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: value must be finite", recordNum, colDistM)
			}
			if dist < 0 {
				return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: value %v must be nonnegative", recordNum, colDistM, dist)
			}
			if prevDistM != nil && dist < *prevDistM {
				return rec, nil, 0, fmt.Errorf("route csv: record %d: field %q: value %v must be nondecreasing (previous %v)", recordNum, colDistM, dist, *prevDistM)
			}
			distCopy := dist
			rec.DistanceM = &distCopy
		}
	}

	if idx.speedSource >= 0 {
		rec.SpeedSource = row[idx.speedSource]
	}

	return rec, tsRat, offsetMs, nil
}

// parseRequiredFloat64 parses a required numeric field, rejecting empty
// values and malformed numeric strings.
func parseRequiredFloat64(s string, field string, recordNum int64) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("route csv: record %d: field %q: value is empty", recordNum, field)
	}
	v, err := parseFloat64Strict(s)
	if err != nil {
		return 0, fmt.Errorf("route csv: record %d: field %q: invalid numeric value %q", recordNum, field, s)
	}
	return v, nil
}

// parseFloat64Strict parses a decimal string into a float64 using exact
// decimal/rational arithmetic rather than strconv.ParseFloat, to avoid
// binary floating point surprises, then converts to float64 for range
// checks (float64 has enough precision for lat/lon/speed/heading ranges).
func parseFloat64Strict(s string) (float64, error) {
	r, ok := parseDecimalToRat(s)
	if !ok {
		return 0, fmt.Errorf("invalid decimal value %q", s)
	}
	f, _ := r.Float64()
	if math.IsInf(f, 0) {
		// Extremely large magnitude decimal strings could overflow
		// float64; treat as invalid rather than silently clamping to
		// +/-Inf.
		return 0, fmt.Errorf("decimal value %q overflows float64 range", s)
	}
	return f, nil
}

// parseDecimalToRat parses a plain (non-exponential) decimal string such as
// "1.0005", "-73.9801730", or "5" into an exact big.Rat. It rejects
// anything that isn't a simple optionally-signed decimal number: no
// exponents, no leading/trailing whitespace, no empty string, no multiple
// signs, no multiple decimal points, and no non-digit characters.
func parseDecimalToRat(s string) (*big.Rat, bool) {
	if s == "" {
		return nil, false
	}

	neg := false
	if s[0] == '+' || s[0] == '-' {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return nil, false
	}

	intPart := s
	fracPart := ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		intPart = s[:dot]
		fracPart = s[dot+1:]
		if strings.IndexByte(fracPart, '.') >= 0 {
			return nil, false
		}
	}

	if intPart == "" && fracPart == "" {
		return nil, false
	}
	if intPart == "" {
		intPart = "0"
	}

	for _, c := range intPart {
		if c < '0' || c > '9' {
			return nil, false
		}
	}
	for _, c := range fracPart {
		if c < '0' || c > '9' {
			return nil, false
		}
	}

	// Build exact rational: sign * (intPart + fracPart) / 10^len(fracPart).
	digits := intPart + fracPart
	num := new(big.Int)
	if _, ok := num.SetString(digits, 10); !ok {
		return nil, false
	}

	denom := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fracPart))), nil)

	r := new(big.Rat).SetFrac(num, denom)
	if neg {
		r.Neg(r)
	}

	return r, true
}

// roundHalfUpMs computes round_half_up(ts * 1000) as an int64, operating on
// the exact rational value of ts to avoid float64 double-rounding
// artifacts.
func roundHalfUpMs(ts *big.Rat) (int64, error) {
	scaled := new(big.Rat).Mul(ts, big.NewRat(1000, 1))

	num := scaled.Num()
	den := scaled.Denom()

	// round_half_up(num/den) for nonnegative num, den > 0:
	//   floor((2*num + den) / (2*den))
	two := big.NewInt(2)
	numerator := new(big.Int).Mul(num, two)
	numerator.Add(numerator, den)
	denominator := new(big.Int).Mul(den, two)

	quotient := new(big.Int).Quo(numerator, denominator)

	if !quotient.IsInt64() {
		return 0, fmt.Errorf("offset_ms overflows int64 range")
	}
	return quotient.Int64(), nil
}

// addTimestamp adds offsetMs to runStartMs, checking for int64 overflow.
func addTimestamp(runStartMs int64, offsetMs int64) (int64, error) {
	sum := runStartMs + offsetMs
	// Overflow check: if offsetMs > 0, sum must be >= runStartMs.
	if offsetMs > 0 && sum < runStartMs {
		return 0, fmt.Errorf("timestamp_ms overflows int64 range")
	}
	if offsetMs < 0 && sum > runStartMs {
		return 0, fmt.Errorf("timestamp_ms overflows int64 range")
	}
	return sum, nil
}
