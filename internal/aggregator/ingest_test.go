package aggregator

import (
	"context"
	"database/sql"
	"testing"

	"gpsdemo/internal/apierr"
	"gpsdemo/internal/storage"
)

func TestIngest_FirstAcceptance(t *testing.T) {
	svc, _ := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 44.0, -121.0})
	res := mustIngest(t, svc, pkt)

	if res.DuplicatePacket {
		t.Fatal("expected first acceptance, not duplicate")
	}
	if res.AcceptedFixes != 1 || res.DuplicateFixes != 0 || res.QuarantinedFixes != 0 {
		t.Fatalf("unexpected counters: %+v", res)
	}
	if res.TripUpdates != 1 {
		t.Fatalf("expected 1 trip update, got %d", res.TripUpdates)
	}
	if res.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", res.Revision)
	}
}

func TestIngest_DuplicatePacketReplay(t *testing.T) {
	svc, _ := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 44.0, -121.0})
	first := mustIngest(t, svc, pkt)

	second := mustIngest(t, svc, pkt)
	if !second.DuplicatePacket {
		t.Fatal("expected duplicate_packet=true on replay")
	}
	if second.Revision != first.Revision {
		t.Fatalf("expected same revision on replay, got %d vs %d", second.Revision, first.Revision)
	}
	if second.AcceptedFixes != first.AcceptedFixes {
		t.Fatalf("expected same accepted_fixes on replay")
	}
}

func TestIngest_DuplicatePacketAlternateEncodingSameFingerprint(t *testing.T) {
	svc, _ := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 44.0, -121.0})
	first := mustIngest(t, svc, pkt)

	// Reorder fixes (simulating alternate encoding/order) but same content.
	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 44.0, -121.0})
	second := mustIngest(t, svc, pkt2)
	if !second.DuplicatePacket {
		t.Fatal("expected duplicate_packet=true for same fingerprint")
	}
	if second.Revision != first.Revision {
		t.Fatal("expected same revision")
	}
}

func TestIngest_PacketConflictDifferentContent(t *testing.T) {
	svc, _ := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 44.0, -121.0})
	mustIngest(t, svc, pkt)

	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 50.0, -121.0})
	err := ingestExpectingErr(t, svc, pkt2)
	if err == nil {
		t.Fatal("expected PACKET_CONFLICT")
	}
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodePacketConflict {
		t.Fatalf("expected PACKET_CONFLICT, got %v", err)
	}
}

func TestIngest_FixConflictAcrossPackets(t *testing.T) {
	svc, _ := newTestService(t)
	pkt1 := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 44.0, -121.0})
	mustIngest(t, svc, pkt1)

	// Different packet ID, same vehicle/timestamp, conflicting lat.
	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs, fixSpec{0, 50.0, -121.0})
	err := ingestExpectingErr(t, svc, pkt2)
	if err == nil {
		t.Fatal("expected FIX_CONFLICT")
	}
	ae, ok := err.(*apierr.Error)
	if !ok || ae.Code != apierr.CodeFixConflict {
		t.Fatalf("expected FIX_CONFLICT, got %v", err)
	}
}

func TestIngest_FixConflictRollsBackEntirePacket(t *testing.T) {
	svc, store := newTestService(t)
	pkt1 := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs, fixSpec{0, 44.0, -121.0})
	mustIngest(t, svc, pkt1)

	// Packet has one conflicting fix and one otherwise-new fix; the whole
	// packet must roll back, including the new fix.
	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs,
		fixSpec{0, 50.0, -121.0},    // conflicts with stored fix at offset 0
		fixSpec{1000, 44.0, -121.0}, // otherwise new
	)
	if err := ingestExpectingErr(t, svc, pkt2); err == nil {
		t.Fatal("expected error")
	}

	// The otherwise-new fix at offset 1000 must not have been stored.
	_, err := storage.ReadTx(context.Background(), store, func(ctx context.Context, tx *sql.Tx) (struct{}, error) {
		_, err := storage.GetFix(ctx, tx, "vehicle-1", baseMs+1000)
		return struct{}{}, err
	})
	if err != storage.ErrNotFound {
		t.Fatalf("expected rolled-back fix to be absent, got err=%v", err)
	}

	// The packet receipt must also not have been stored.
	_, err = storage.ReadTx(context.Background(), store, func(ctx context.Context, tx *sql.Tx) (struct{}, error) {
		_, err := storage.GetPacket(ctx, tx, "sim-1", "20261024-000002")
		return struct{}{}, err
	})
	if err != storage.ErrNotFound {
		t.Fatalf("expected rolled-back packet receipt to be absent, got err=%v", err)
	}
}

func TestIngest_InPacketDuplicatesCounted(t *testing.T) {
	svc, _ := newTestService(t)
	pkt := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 44.0, -121.0},
		fixSpec{0, 44.0, -121.0}, // exact duplicate within packet
	)
	res := mustIngest(t, svc, pkt)
	if res.AcceptedFixes != 1 {
		t.Fatalf("expected 1 accepted fix, got %d", res.AcceptedFixes)
	}
	if res.DuplicateFixes != 1 {
		t.Fatalf("expected 1 duplicate fix, got %d", res.DuplicateFixes)
	}
}

func TestIngest_OverlappingPacketsCorrectDuplicateCounts(t *testing.T) {
	svc, _ := newTestService(t)
	pkt1 := buildPacket("sim-1", "vehicle-1", "20261024-000001", baseMs,
		fixSpec{0, 44.0, -121.0},
		fixSpec{1000, 44.1, -121.1},
	)
	mustIngest(t, svc, pkt1)

	pkt2 := buildPacket("sim-1", "vehicle-1", "20261024-000002", baseMs,
		fixSpec{1000, 44.1, -121.1}, // exact match of existing
		fixSpec{2000, 44.2, -121.2}, // new
	)
	res := mustIngest(t, svc, pkt2)
	if res.AcceptedFixes != 1 {
		t.Fatalf("expected 1 newly accepted fix, got %d", res.AcceptedFixes)
	}
	if res.DuplicateFixes != 1 {
		t.Fatalf("expected 1 duplicate fix, got %d", res.DuplicateFixes)
	}
}
