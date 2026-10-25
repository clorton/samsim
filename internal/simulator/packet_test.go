package simulator

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	telemetryv1 "gpsdemo/gen/telemetry/v1"
)

func sampleFixes() []RouteRecord {
	return []RouteRecord{
		{
			TimestampMs:    1_700_000_000_000,
			Latitude:       40.7128,
			Longitude:      -74.0060,
			SpeedMps:       12.5,
			HeadingDegrees: 90.0,
		},
		{
			TimestampMs:    1_700_000_001_000,
			Latitude:       40.7129,
			Longitude:      -74.0061,
			SpeedMps:       13.0,
			HeadingDegrees: 91.5,
		},
	}
}

func assertFixesMatch(t *testing.T, got *telemetryv1.TelemetryPacket, want []RouteRecord, simulatorID, vehicleID, packetID string) {
	t.Helper()
	if got.GetSimulatorId() != simulatorID {
		t.Errorf("simulator_id = %q, want %q", got.GetSimulatorId(), simulatorID)
	}
	if got.GetVehicleId() != vehicleID {
		t.Errorf("vehicle_id = %q, want %q", got.GetVehicleId(), vehicleID)
	}
	if got.GetPacketId() != packetID {
		t.Errorf("packet_id = %q, want %q", got.GetPacketId(), packetID)
	}
	if len(got.GetFixes()) != len(want) {
		t.Fatalf("fix count = %d, want %d", len(got.GetFixes()), len(want))
	}
	for i, f := range got.GetFixes() {
		w := want[i]
		if f.GetTimestampMs() != w.TimestampMs {
			t.Errorf("fix[%d].timestamp_ms = %d, want %d", i, f.GetTimestampMs(), w.TimestampMs)
		}
		if f.GetLatitude() != w.Latitude {
			t.Errorf("fix[%d].latitude = %v, want %v", i, f.GetLatitude(), w.Latitude)
		}
		if f.GetLongitude() != w.Longitude {
			t.Errorf("fix[%d].longitude = %v, want %v", i, f.GetLongitude(), w.Longitude)
		}
		if f.GetSpeedMps() != w.SpeedMps {
			t.Errorf("fix[%d].speed_mps = %v, want %v", i, f.GetSpeedMps(), w.SpeedMps)
		}
		if f.GetHeadingDegrees() != w.HeadingDegrees {
			t.Errorf("fix[%d].heading_degrees = %v, want %v", i, f.GetHeadingDegrees(), w.HeadingDegrees)
		}
	}
}

func TestBuildPacketJSON(t *testing.T) {
	fixes := sampleFixes()
	pkt, err := BuildPacket("pkt-1", "sim-001", "veh-001", fixes, EncodingJSON)
	if err != nil {
		t.Fatalf("BuildPacket: %v", err)
	}
	if pkt.Encoding != EncodingJSON {
		t.Errorf("Encoding = %v, want %v", pkt.Encoding, EncodingJSON)
	}
	if pkt.FixCount != len(fixes) {
		t.Errorf("FixCount = %d, want %d", pkt.FixCount, len(fixes))
	}

	var decoded telemetryv1.TelemetryPacket
	if err := protojson.Unmarshal(pkt.Body, &decoded); err != nil {
		t.Fatalf("protojson.Unmarshal: %v (body=%s)", err, pkt.Body)
	}
	assertFixesMatch(t, &decoded, fixes, "sim-001", "veh-001", "pkt-1")
}

func TestBuildPacketProtobuf(t *testing.T) {
	fixes := sampleFixes()
	pkt, err := BuildPacket("pkt-2", "sim-002", "veh-002", fixes, EncodingProtobuf)
	if err != nil {
		t.Fatalf("BuildPacket: %v", err)
	}
	if pkt.Encoding != EncodingProtobuf {
		t.Errorf("Encoding = %v, want %v", pkt.Encoding, EncodingProtobuf)
	}

	var decoded telemetryv1.TelemetryPacket
	if err := proto.Unmarshal(pkt.Body, &decoded); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	assertFixesMatch(t, &decoded, fixes, "sim-002", "veh-002", "pkt-2")
}

func TestBuildPacketEmptyFixes(t *testing.T) {
	_, err := BuildPacket("pkt-3", "sim-001", "veh-001", nil, EncodingJSON)
	if err == nil {
		t.Fatal("expected error for empty fixes, got nil")
	}
}

func TestBuildPacketNonAscendingTimestamps(t *testing.T) {
	fixes := []RouteRecord{
		{TimestampMs: 1000, Latitude: 1, Longitude: 1, SpeedMps: 1, HeadingDegrees: 1},
		{TimestampMs: 1000, Latitude: 2, Longitude: 2, SpeedMps: 2, HeadingDegrees: 2},
	}
	if _, err := BuildPacket("pkt-4", "sim-001", "veh-001", fixes, EncodingJSON); err == nil {
		t.Fatal("expected error for equal timestamps, got nil")
	}

	fixesDesc := []RouteRecord{
		{TimestampMs: 2000, Latitude: 1, Longitude: 1, SpeedMps: 1, HeadingDegrees: 1},
		{TimestampMs: 1000, Latitude: 2, Longitude: 2, SpeedMps: 2, HeadingDegrees: 2},
	}
	if _, err := BuildPacket("pkt-5", "sim-001", "veh-001", fixesDesc, EncodingJSON); err == nil {
		t.Fatal("expected error for descending timestamps, got nil")
	}
}

func TestPacketTimestampRange(t *testing.T) {
	fixes := sampleFixes()
	pkt, err := BuildPacket("pkt-6", "sim-001", "veh-001", fixes, EncodingJSON)
	if err != nil {
		t.Fatalf("BuildPacket: %v", err)
	}
	if pkt.FirstTimestampMs != fixes[0].TimestampMs {
		t.Errorf("FirstTimestampMs = %d, want %d", pkt.FirstTimestampMs, fixes[0].TimestampMs)
	}
	if pkt.LastTimestampMs != fixes[len(fixes)-1].TimestampMs {
		t.Errorf("LastTimestampMs = %d, want %d", pkt.LastTimestampMs, fixes[len(fixes)-1].TimestampMs)
	}
}

func TestEncodingContentType(t *testing.T) {
	if got := EncodingJSON.ContentType(); got != "application/json" {
		t.Errorf("EncodingJSON.ContentType() = %q, want %q", got, "application/json")
	}
	if got := EncodingProtobuf.ContentType(); got != "application/x-protobuf" {
		t.Errorf("EncodingProtobuf.ContentType() = %q, want %q", got, "application/x-protobuf")
	}
}
