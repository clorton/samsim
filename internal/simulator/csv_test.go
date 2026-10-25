package simulator

import (
	"strings"
	"testing"
)

const sampleCSV = `ts,lat,lon,speed_mps,heading_deg,dist_m,speed_source
0.0,40.7440080,-73.9801730,4.09,29.6,0.0,osrm_estimate
1.0,40.7440406,-73.9801489,4.21,29.0,4.2,osrm_estimate
2.0,40.7440737,-73.9801247,4.21,29.0,8.4,osrm_estimate
`

func defaultOpts() LoadRouteCSVOptions {
	return LoadRouteCSVOptions{
		ResolvedRunStartMs: 1_700_000_000_000,
		MaxBytes:           1 << 20,
		MaxRows:            10000,
	}
}

func TestLoadRouteCSV_SampleFile(t *testing.T) {
	opts := defaultOpts()
	recs, err := LoadRouteCSV([]byte(sampleCSV), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recs))
	}

	wantTs := []int64{
		opts.ResolvedRunStartMs + 0,
		opts.ResolvedRunStartMs + 1000,
		opts.ResolvedRunStartMs + 2000,
	}
	for i, want := range wantTs {
		if recs[i].TimestampMs != want {
			t.Errorf("record %d: TimestampMs = %d, want %d", i, recs[i].TimestampMs, want)
		}
	}

	if recs[0].Latitude != 40.7440080 {
		t.Errorf("record 0 latitude = %v, want 40.7440080", recs[0].Latitude)
	}
	if recs[0].Longitude != -73.9801730 {
		t.Errorf("record 0 longitude = %v, want -73.9801730", recs[0].Longitude)
	}
	if recs[0].SpeedMps != float32(4.09) {
		t.Errorf("record 0 speed_mps = %v, want 4.09", recs[0].SpeedMps)
	}
	if recs[0].HeadingDegrees != float32(29.6) {
		t.Errorf("record 0 heading_deg = %v, want 29.6", recs[0].HeadingDegrees)
	}
	if recs[0].DistanceM == nil || *recs[0].DistanceM != 0.0 {
		t.Errorf("record 0 dist_m = %v, want 0.0", recs[0].DistanceM)
	}
	if recs[0].SpeedSource != "osrm_estimate" {
		t.Errorf("record 0 speed_source = %q, want osrm_estimate", recs[0].SpeedSource)
	}
}

func TestLoadRouteCSV_ReorderedColumns(t *testing.T) {
	csvData := "lat,speed_mps,ts,heading_deg,lon\n" +
		"40.0,4.09,0.0,29.6,-73.0\n" +
		"40.1,4.21,1.0,29.0,-73.1\n"
	opts := defaultOpts()
	recs, err := LoadRouteCSV([]byte(csvData), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].Latitude != 40.0 || recs[0].Longitude != -73.0 {
		t.Errorf("record 0 lat/lon = %v/%v, want 40.0/-73.0", recs[0].Latitude, recs[0].Longitude)
	}
	if recs[1].TimestampMs != opts.ResolvedRunStartMs+1000 {
		t.Errorf("record 1 TimestampMs = %d, want %d", recs[1].TimestampMs, opts.ResolvedRunStartMs+1000)
	}
}

func TestLoadRouteCSV_QuotedFieldsAndCRLF(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg,speed_source\r\n" +
		"0.0,40.0,-73.0,4.09,29.6,\"osrm, estimate\"\r\n" +
		"1.0,40.1,-73.1,4.21,29.0,\"quoted \"\"value\"\"\"\r\n"
	recs, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].SpeedSource != "osrm, estimate" {
		t.Errorf("record 0 speed_source = %q, want %q", recs[0].SpeedSource, "osrm, estimate")
	}
	if recs[1].SpeedSource != `quoted "value"` {
		t.Errorf("record 1 speed_source = %q, want %q", recs[1].SpeedSource, `quoted "value"`)
	}
}

func TestLoadRouteCSV_UTF8BOMStripped(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	data := append(bom, []byte(sampleCSV)...)
	recs, err := LoadRouteCSV(data, defaultOpts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recs))
	}
}

func TestLoadRouteCSV_MissingRequiredHeader(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps\n0.0,40.0,-73.0,4.09\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for missing heading_deg header, got nil")
	}
	if !strings.Contains(err.Error(), "heading_deg") {
		t.Errorf("error should mention missing field heading_deg, got: %v", err)
	}
}

func TestLoadRouteCSV_DuplicateHeader(t *testing.T) {
	csvData := "ts,lat,lat,lon,speed_mps,heading_deg\n0.0,40.0,40.1,-73.0,4.09,29.6\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for duplicate lat header, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate header, got: %v", err)
	}
}

func TestLoadRouteCSV_EmptyRequiredField(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\n0.0,,-73.0,4.09,29.6\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for empty lat field, got nil")
	}
	if !strings.Contains(err.Error(), "record 1") || !strings.Contains(err.Error(), "lat") {
		t.Errorf("error should mention record 1 and field lat, got: %v", err)
	}
}

func TestLoadRouteCSV_OutOfRangeValues(t *testing.T) {
	tests := []struct {
		name string
		csv  string
	}{
		{"lat_too_high", "ts,lat,lon,speed_mps,heading_deg\n0.0,91.0,-73.0,4.09,29.6\n"},
		{"lat_too_low", "ts,lat,lon,speed_mps,heading_deg\n0.0,-91.0,-73.0,4.09,29.6\n"},
		{"lon_too_high", "ts,lat,lon,speed_mps,heading_deg\n0.0,40.0,181.0,4.09,29.6\n"},
		{"lon_too_low", "ts,lat,lon,speed_mps,heading_deg\n0.0,40.0,-181.0,4.09,29.6\n"},
		{"negative_speed", "ts,lat,lon,speed_mps,heading_deg\n0.0,40.0,-73.0,-1.0,29.6\n"},
		{"heading_360", "ts,lat,lon,speed_mps,heading_deg\n0.0,40.0,-73.0,4.09,360.0\n"},
		{"heading_negative", "ts,lat,lon,speed_mps,heading_deg\n0.0,40.0,-73.0,4.09,-1.0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadRouteCSV([]byte(tt.csv), defaultOpts())
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestLoadRouteCSV_MalformedNumericString(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\n0.0,not-a-number,-73.0,4.09,29.6\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for malformed lat value, got nil")
	}
	if !strings.Contains(err.Error(), "lat") {
		t.Errorf("error should mention field lat, got: %v", err)
	}
}

func TestLoadRouteCSV_MalformedTSString(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\nabc,40.0,-73.0,4.09,29.6\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for malformed ts value, got nil")
	}
}

func TestLoadRouteCSV_TSZeroMapsToRunStart(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\n0.0,40.0,-73.0,4.09,29.6\n"
	opts := defaultOpts()
	recs, err := LoadRouteCSV([]byte(csvData), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recs[0].TimestampMs != opts.ResolvedRunStartMs {
		t.Errorf("TimestampMs = %d, want exactly ResolvedRunStartMs %d", recs[0].TimestampMs, opts.ResolvedRunStartMs)
	}
}

func TestLoadRouteCSV_FirstTSNonZeroNotRebased(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\n5.0,40.0,-73.0,4.09,29.6\n6.0,40.1,-73.1,4.21,29.0\n"
	opts := defaultOpts()
	recs, err := LoadRouteCSV([]byte(csvData), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := opts.ResolvedRunStartMs + 5000
	if recs[0].TimestampMs != want {
		t.Errorf("TimestampMs = %d, want %d (no rebasing of first ts)", recs[0].TimestampMs, want)
	}
	want1 := opts.ResolvedRunStartMs + 6000
	if recs[1].TimestampMs != want1 {
		t.Errorf("record 1 TimestampMs = %d, want %d", recs[1].TimestampMs, want1)
	}
}

func TestLoadRouteCSV_HalfUpRounding(t *testing.T) {
	// ts=1.0005 must round half-up to exactly 1001ms (not 1000ms as a naive
	// float64 round-to-even or double-rounding bug might produce).
	csvData := "ts,lat,lon,speed_mps,heading_deg\n1.0005,40.0,-73.0,4.09,29.6\n"
	opts := defaultOpts()
	recs, err := LoadRouteCSV([]byte(csvData), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := opts.ResolvedRunStartMs + 1001
	if recs[0].TimestampMs != want {
		t.Errorf("TimestampMs = %d, want %d (half-up rounding of 1.0005s)", recs[0].TimestampMs, want)
	}
}

func TestLoadRouteCSV_RepeatedImpreciseDecimal(t *testing.T) {
	// 0.1 is not exactly representable in binary floating point, but should
	// still produce an exact offset_ms of 100 when parsed via decimal
	// arithmetic. Verify many sequential increments of 0.1 don't drift.
	var b strings.Builder
	b.WriteString("ts,lat,lon,speed_mps,heading_deg\n")
	n := 50
	for i := 1; i <= n; i++ {
		// Build ts values 0.1, 0.2, 0.3, ... i*0.1 as exact decimal strings.
		b.WriteString(decimalTimesPointOne(i))
		b.WriteString(",40.0,-73.0,4.09,29.6\n")
	}
	opts := defaultOpts()
	recs, err := LoadRouteCSV([]byte(b.String()), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != n {
		t.Fatalf("expected %d records, got %d", n, len(recs))
	}
	for i := 0; i < n; i++ {
		wantOffset := int64(i+1) * 100
		want := opts.ResolvedRunStartMs + wantOffset
		if recs[i].TimestampMs != want {
			t.Errorf("record %d: TimestampMs = %d, want %d (offset %dms)", i, recs[i].TimestampMs, want, wantOffset)
		}
	}
}

// decimalTimesPointOne returns the exact decimal string representation of
// i * 0.1, e.g. decimalTimesPointOne(3) == "0.3", decimalTimesPointOne(12)
// == "1.2".
func decimalTimesPointOne(i int) string {
	whole := i / 10
	frac := i % 10
	return itoa(whole) + "." + itoa(frac)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestLoadRouteCSV_NonIncreasingTS(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\n1.0,40.0,-73.0,4.09,29.6\n1.0,40.1,-73.1,4.21,29.0\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for duplicate ts values, got nil")
	}
	if !strings.Contains(err.Error(), "increasing") {
		t.Errorf("error should mention strictly increasing requirement, got: %v", err)
	}
}

func TestLoadRouteCSV_DecreasingTS(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\n2.0,40.0,-73.0,4.09,29.6\n1.0,40.1,-73.1,4.21,29.0\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for decreasing ts values, got nil")
	}
}

func TestLoadRouteCSV_RoundingCollision(t *testing.T) {
	// 1.0001 and 1.0004 both round half-up to 1000ms, but are distinct,
	// strictly increasing source ts values. The rounded offsets collide and
	// must be rejected.
	csvData := "ts,lat,lon,speed_mps,heading_deg\n1.0001,40.0,-73.0,4.09,29.6\n1.0004,40.1,-73.1,4.21,29.0\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for rounding collision, got nil")
	}
	if !strings.Contains(err.Error(), "collides") {
		t.Errorf("error should mention rounding collision, got: %v", err)
	}
}

func TestLoadRouteCSV_ByteSizeLimitExceeded(t *testing.T) {
	opts := defaultOpts()
	opts.MaxBytes = 10 // sampleCSV is much larger than this
	_, err := LoadRouteCSV([]byte(sampleCSV), opts)
	if err == nil {
		t.Fatal("expected error for exceeding byte size limit, got nil")
	}
}

func TestLoadRouteCSV_RowCountLimitExceeded(t *testing.T) {
	opts := defaultOpts()
	opts.MaxRows = 2 // sampleCSV has 3 data rows
	_, err := LoadRouteCSV([]byte(sampleCSV), opts)
	if err == nil {
		t.Fatal("expected error for exceeding row count limit, got nil")
	}
}

func TestLoadRouteCSV_EmptyRoute(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for empty route (header only), got nil")
	}
}

func TestLoadRouteCSV_OptionalColumnsPresent(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg,dist_m,speed_source\n" +
		"0.0,40.0,-73.0,4.09,29.6,0.0,osrm_estimate\n" +
		"1.0,40.1,-73.1,4.21,29.0,4.2,osrm_estimate\n"
	recs, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recs[0].DistanceM == nil || *recs[0].DistanceM != 0.0 {
		t.Errorf("record 0 dist_m = %v, want 0.0", recs[0].DistanceM)
	}
	if recs[1].DistanceM == nil || *recs[1].DistanceM != 4.2 {
		t.Errorf("record 1 dist_m = %v, want 4.2", recs[1].DistanceM)
	}
}

func TestLoadRouteCSV_DistMNonFinite(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg,dist_m\n0.0,40.0,-73.0,4.09,29.6,not-a-number\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for non-numeric dist_m, got nil")
	}
	if !strings.Contains(err.Error(), "dist_m") {
		t.Errorf("error should mention dist_m, got: %v", err)
	}
}

func TestLoadRouteCSV_DistMNotNondecreasing(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg,dist_m\n" +
		"0.0,40.0,-73.0,4.09,29.6,10.0\n" +
		"1.0,40.1,-73.1,4.21,29.0,5.0\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for decreasing dist_m, got nil")
	}
	if !strings.Contains(err.Error(), "nondecreasing") {
		t.Errorf("error should mention nondecreasing, got: %v", err)
	}
}

func TestLoadRouteCSV_OptionalColumnsAbsent(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\n0.0,40.0,-73.0,4.09,29.6\n"
	recs, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recs[0].DistanceM != nil {
		t.Errorf("record 0 DistanceM = %v, want nil", recs[0].DistanceM)
	}
	if recs[0].SpeedSource != "" {
		t.Errorf("record 0 SpeedSource = %q, want empty", recs[0].SpeedSource)
	}
}

func TestLoadRouteCSV_UnknownExtraColumnIgnored(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg,dist_m,speed_source,extra_column\n" +
		"0.0,40.0,-73.0,4.09,29.6,0.0,osrm_estimate,whatever\n"
	recs, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err != nil {
		t.Fatalf("unexpected error for extra unknown column: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
}

func TestLoadRouteCSV_InconsistentRecordWidth(t *testing.T) {
	csvData := "ts,lat,lon,speed_mps,heading_deg\n0.0,40.0,-73.0,4.09,29.6\n1.0,40.1,-73.1,4.21\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for inconsistent record width, got nil")
	}
	if !strings.Contains(err.Error(), "width") {
		t.Errorf("error should mention record width, got: %v", err)
	}
}

func TestLoadRouteCSV_MalformedCSV(t *testing.T) {
	// Unterminated quoted field.
	csvData := "ts,lat,lon,speed_mps,heading_deg\n0.0,\"40.0,-73.0,4.09,29.6\n"
	_, err := LoadRouteCSV([]byte(csvData), defaultOpts())
	if err == nil {
		t.Fatal("expected error for malformed CSV, got nil")
	}
}
