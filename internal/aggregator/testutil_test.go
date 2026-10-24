package aggregator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gpsdemo/internal/clock"
	"gpsdemo/internal/events"
	"gpsdemo/internal/httpapi"
	"gpsdemo/internal/idgen"
	"gpsdemo/internal/storage"

	telemetryv1 "gpsdemo/gen/telemetry/v1"
)

const testGapSeconds = 600

func newTestService(t *testing.T) (*Service, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(context.Background(), storage.Options{
		Path:                  filepath.Join(dir, "test.sqlite"),
		BusyTimeoutMs:         5000,
		TripGapSeconds:        testGapSeconds,
		ExpectedFixIntervalMs: 1000,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	hub := events.NewHub(16)
	clk := clock.NewFixed(time.Unix(1700000000, 0).UTC())
	ids := idgen.NewSequential("trip-")
	svc := NewService(store, hub, clk, ids, 10000)
	return svc, store
}

// buildPacket constructs a valid TelemetryPacket DTO for tests; fixes is a
// list of (timestampOffsetMs, lat, lon) triples relative to baseMs.
type fixSpec struct {
	offsetMs int64
	lat, lon float64
}

func buildPacket(simulatorID, vehicleID, packetID string, baseMs int64, specs ...fixSpec) *telemetryv1.TelemetryPacket {
	pkt := &telemetryv1.TelemetryPacket{
		SimulatorId: simulatorID,
		VehicleId:   vehicleID,
		PacketId:    packetID,
	}
	for _, s := range specs {
		pkt.Fixes = append(pkt.Fixes, &telemetryv1.GPSFix{
			TimestampMs:    baseMs + s.offsetMs,
			Latitude:       s.lat,
			Longitude:      s.lon,
			SpeedMps:       1,
			HeadingDegrees: 0,
		})
	}
	return pkt
}

func mustIngest(t *testing.T, svc *Service, pkt *telemetryv1.TelemetryPacket) *IngestResult {
	t.Helper()
	if err := httpapi.ValidatePacketSemantics(pkt, svc.MaxFixesPerPacket); err != nil {
		t.Fatalf("validate: %v", err)
	}
	np, err := httpapi.NormalizePacket(pkt)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	res, err := svc.Ingest(context.Background(), np)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return res
}

func ingestExpectingErr(t *testing.T, svc *Service, pkt *telemetryv1.TelemetryPacket) error {
	t.Helper()
	if err := httpapi.ValidatePacketSemantics(pkt, svc.MaxFixesPerPacket); err != nil {
		return err
	}
	np, err := httpapi.NormalizePacket(pkt)
	if err != nil {
		return err
	}
	_, err = svc.Ingest(context.Background(), np)
	return err
}

const baseMs = int64(1792845600000) // positive base timestamp, per AG-10 guidance
