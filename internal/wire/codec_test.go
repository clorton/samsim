package wire

import (
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/proto"

	"gpsdemo/internal/apierr"

	telemetryv1 "gpsdemo/gen/telemetry/v1"
)

func TestDecodeJSONPacket_Basic(t *testing.T) {
	body := []byte(`{
		"simulatorId": "sim-001",
		"vehicleId": "vehicle-001",
		"packetId": "20261024-000001",
		"fixes": [
			{"timestampMs":"1792845600000","latitude":44.0582,"longitude":-121.3153,"speedMps":5,"headingDegrees":90}
		]
	}`)
	pkt, err := DecodeJSONPacket(body)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if pkt.GetSimulatorId() != "sim-001" || pkt.GetVehicleId() != "vehicle-001" {
		t.Fatalf("unexpected decoded packet: %+v", pkt)
	}
	if len(pkt.GetFixes()) != 1 || pkt.GetFixes()[0].GetTimestampMs() != 1792845600000 {
		t.Fatalf("unexpected fixes: %+v", pkt.GetFixes())
	}
}

func TestDecodeJSONPacket_UnknownField(t *testing.T) {
	body := []byte(`{"simulatorId":"sim-001","vehicleId":"v-1","packetId":"20261024-000001","fixes":[],"bogus":1}`)
	if _, err := DecodeJSONPacket(body); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestDecodeJSONPacket_DuplicateKeys(t *testing.T) {
	body := []byte(`{"simulatorId":"sim-001","simulatorId":"sim-002","vehicleId":"v-1","packetId":"20261024-000001","fixes":[]}`)
	if _, err := DecodeJSONPacket(body); err == nil {
		t.Fatal("expected error for duplicate keys")
	}
}

func TestDecodeJSONPacket_TrailingData(t *testing.T) {
	body := []byte(`{"simulatorId":"sim-001","vehicleId":"v-1","packetId":"20261024-000001","fixes":[]} {}`)
	if _, err := DecodeJSONPacket(body); err == nil {
		t.Fatal("expected error for trailing data")
	}
}

func TestDecodeJSONPacket_MalformedJSON(t *testing.T) {
	body := []byte(`{"simulatorId":`)
	if _, err := DecodeJSONPacket(body); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestDecodeProtobufPacket_UnknownField(t *testing.T) {
	pkt := &telemetryv1.TelemetryPacket{
		SimulatorId: "sim-001",
		VehicleId:   "v-1",
		PacketId:    "20261024-000001",
	}
	data, err := proto.Marshal(pkt)
	if err != nil {
		t.Fatal(err)
	}
	// Append an unknown field (field number 99, varint type) by hand.
	unknown, _ := hex.DecodeString("f805")
	data = append(data, unknown...)
	if _, err := DecodeProtobufPacket(data); err == nil {
		t.Fatal("expected error for unknown protobuf field")
	}
}

func TestValidatePacketSemantics(t *testing.T) {
	base := &telemetryv1.TelemetryPacket{
		SimulatorId: "sim-001",
		VehicleId:   "vehicle-001",
		PacketId:    "20261024-000001",
		Fixes: []*telemetryv1.GPSFix{
			{TimestampMs: 1792845600000, Latitude: 44.0582, Longitude: -121.3153, SpeedMps: 5, HeadingDegrees: 90},
		},
	}
	if err := ValidatePacketSemantics(base, 10000); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*telemetryv1.TelemetryPacket)
	}{
		{"zero timestamp", func(p *telemetryv1.TelemetryPacket) { p.Fixes[0].TimestampMs = 0 }},
		{"bad latitude", func(p *telemetryv1.TelemetryPacket) { p.Fixes[0].Latitude = 91 }},
		{"bad longitude", func(p *telemetryv1.TelemetryPacket) { p.Fixes[0].Longitude = 181 }},
		{"negative speed", func(p *telemetryv1.TelemetryPacket) { p.Fixes[0].SpeedMps = -1 }},
		{"heading 360", func(p *telemetryv1.TelemetryPacket) { p.Fixes[0].HeadingDegrees = 360 }},
		{"no fixes", func(p *telemetryv1.TelemetryPacket) { p.Fixes = nil }},
		{"bad simulator id", func(p *telemetryv1.TelemetryPacket) { p.SimulatorId = "bad id!" }},
		{"bad packet id", func(p *telemetryv1.TelemetryPacket) { p.PacketId = "not-a-packet-id" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := proto.Clone(base).(*telemetryv1.TelemetryPacket)
			c.mutate(p)
			if err := ValidatePacketSemantics(p, 10000); err == nil {
				t.Fatalf("expected validation error for %s", c.name)
			}
		})
	}
}

func TestNormalizePacket_SortsAndDedupes(t *testing.T) {
	pkt := &telemetryv1.TelemetryPacket{
		SimulatorId: "sim-001",
		VehicleId:   "vehicle-001",
		PacketId:    "20261024-000001",
		Fixes: []*telemetryv1.GPSFix{
			{TimestampMs: 2000, Latitude: 1, Longitude: 2},
			{TimestampMs: 1000, Latitude: 3, Longitude: 4},
			{TimestampMs: 1000, Latitude: 3, Longitude: 4}, // exact duplicate
		},
	}
	np, err := NormalizePacket(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if len(np.Fixes) != 2 {
		t.Fatalf("expected 2 unique fixes, got %d", len(np.Fixes))
	}
	if np.Fixes[0].TimestampMs != 1000 || np.Fixes[1].TimestampMs != 2000 {
		t.Fatalf("expected sorted order, got %+v", np.Fixes)
	}
	if np.InPacketDuplicateFixes != 1 {
		t.Fatalf("expected 1 duplicate, got %d", np.InPacketDuplicateFixes)
	}
	if np.OriginalFixCount != 3 {
		t.Fatalf("expected original count 3, got %d", np.OriginalFixCount)
	}
}

func TestNormalizePacket_ConflictingRepeats(t *testing.T) {
	pkt := &telemetryv1.TelemetryPacket{
		SimulatorId: "sim-001",
		VehicleId:   "vehicle-001",
		PacketId:    "20261024-000001",
		Fixes: []*telemetryv1.GPSFix{
			{TimestampMs: 1000, Latitude: 3, Longitude: 4},
			{TimestampMs: 1000, Latitude: 5, Longitude: 6}, // conflicting
		},
	}
	_, err := NormalizePacket(pkt)
	if err == nil {
		t.Fatal("expected FIX_CONFLICT error")
	}
	de, ok := err.(*apierr.Error)
	if !ok || de.Code != "FIX_CONFLICT" {
		t.Fatalf("expected FIX_CONFLICT, got %v", err)
	}
}

func TestFingerprint_InvariantUnderEncodingOrderAndDuplicates(t *testing.T) {
	a := &telemetryv1.TelemetryPacket{
		SimulatorId: "sim-001",
		VehicleId:   "vehicle-001",
		PacketId:    "20261024-000001",
		Fixes: []*telemetryv1.GPSFix{
			{TimestampMs: 2000, Latitude: 1, Longitude: 2, SpeedMps: 0, HeadingDegrees: 0},
			{TimestampMs: 1000, Latitude: 3, Longitude: 4},
		},
	}
	b := &telemetryv1.TelemetryPacket{
		SimulatorId: "sim-001",
		VehicleId:   "vehicle-001",
		PacketId:    "20261024-000001",
		Fixes: []*telemetryv1.GPSFix{
			{TimestampMs: 1000, Latitude: 3, Longitude: 4},
			{TimestampMs: 1000, Latitude: 3, Longitude: 4}, // duplicate, should collapse
			{TimestampMs: 2000, Latitude: 1, Longitude: 2}, // explicit zero omitted == same
		},
	}
	na, err := NormalizePacket(a)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := NormalizePacket(b)
	if err != nil {
		t.Fatal(err)
	}
	fa, err := na.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fb, err := nb.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fa != fb {
		t.Fatalf("expected equal fingerprints, got %x vs %x", fa, fb)
	}
}

func TestFingerprint_DiffersOnContent(t *testing.T) {
	a := &telemetryv1.TelemetryPacket{
		SimulatorId: "sim-001", VehicleId: "vehicle-001", PacketId: "20261024-000001",
		Fixes: []*telemetryv1.GPSFix{{TimestampMs: 1000, Latitude: 1}},
	}
	b := &telemetryv1.TelemetryPacket{
		SimulatorId: "sim-001", VehicleId: "vehicle-001", PacketId: "20261024-000001",
		Fixes: []*telemetryv1.GPSFix{{TimestampMs: 1000, Latitude: 2}},
	}
	na, _ := NormalizePacket(a)
	nb, _ := NormalizePacket(b)
	fa, _ := na.Fingerprint()
	fb, _ := nb.Fingerprint()
	if fa == fb {
		t.Fatal("expected different fingerprints for different content")
	}
}

func TestValidatePacketID(t *testing.T) {
	valid := []string{"20261024-1", "20261024-000001", "20000229-99999999999999999999"[:len("20000229-")+20]}
	for _, v := range valid {
		if err := ValidatePacketID(v); err != nil {
			t.Errorf("expected %q valid, got %v", v, err)
		}
	}
	invalid := []string{"2026-10-24-1", "20261024", "20261024-", "20261301-1", "abc"}
	for _, v := range invalid {
		if err := ValidatePacketID(v); err == nil {
			t.Errorf("expected %q invalid", v)
		}
	}
}
