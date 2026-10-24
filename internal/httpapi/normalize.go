package httpapi

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"

	telemetryv1 "gpsdemo/gen/telemetry/v1"
)

// NormalizedFix is one validated, canonicalized fix ready for comparison,
// fingerprinting and storage.
type NormalizedFix struct {
	TimestampMs    int64
	Latitude       float64
	Longitude      float64
	SpeedMps       float32
	HeadingDegrees float32
}

// Equal reports whether two normalized fixes are identical in all
// measurement fields (not just timestamp). Comparison uses exact equality
// post float32 conversion for speed/heading, per AG-02 ("do not use epsilon
// equality for duplicate detection").
func (a NormalizedFix) Equal(b NormalizedFix) bool {
	return a.TimestampMs == b.TimestampMs &&
		a.Latitude == b.Latitude &&
		a.Longitude == b.Longitude &&
		a.SpeedMps == b.SpeedMps &&
		a.HeadingDegrees == b.HeadingDegrees
}

// NormalizedPacket is a packet whose fixes have been sorted by timestamp,
// deduplicated (identical repeats collapsed), checked for same-timestamp
// conflicts, and whose measurement fields have been canonicalized
// (negative zero -> positive zero, float32 rounding applied to
// speed/heading).
type NormalizedPacket struct {
	SimulatorID string
	VehicleID   string
	PacketID    string
	Fixes       []NormalizedFix // sorted ascending by TimestampMs, unique

	// OriginalFixCount is the number of fixes in the input before
	// collapsing identical repeats, used for the initial response's
	// accepted+duplicate accounting.
	OriginalFixCount int
	// InPacketDuplicateFixes counts input fixes collapsed as exact repeats
	// of another fix in the same packet.
	InPacketDuplicateFixes int
}

func canonicalizeZero(f float64) float64 {
	if f == 0 {
		return 0
	}
	return f
}

// NormalizePacket sorts fixes by timestamp, canonicalizes measurement
// fields, collapses identical repeated fixes (counting them as duplicates),
// and rejects conflicting repeated timestamps with FIX_CONFLICT (AG-04 step
// 1). Call ValidatePacketSemantics first.
func NormalizePacket(pkt *telemetryv1.TelemetryPacket) (*NormalizedPacket, error) {
	raw := pkt.GetFixes()
	normalized := make([]NormalizedFix, len(raw))
	for i, f := range raw {
		normalized[i] = NormalizedFix{
			TimestampMs:    f.GetTimestampMs(),
			Latitude:       canonicalizeZero(f.GetLatitude()),
			Longitude:      canonicalizeZero(f.GetLongitude()),
			SpeedMps:       float32(canonicalizeZero(float64(f.GetSpeedMps()))),
			HeadingDegrees: float32(canonicalizeZero(float64(f.GetHeadingDegrees()))),
		}
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].TimestampMs < normalized[j].TimestampMs
	})

	var deduped []NormalizedFix
	dupCount := 0
	for i, f := range normalized {
		if i > 0 && f.TimestampMs == normalized[i-1].TimestampMs {
			if f.Equal(normalized[i-1]) {
				dupCount++
				continue
			}
			return nil, newDecodeErr("FIX_CONFLICT", "conflicting repeated fix at timestamp_ms=%d within packet", f.TimestampMs)
		}
		deduped = append(deduped, f)
	}

	return &NormalizedPacket{
		SimulatorID:            pkt.GetSimulatorId(),
		VehicleID:              pkt.GetVehicleId(),
		PacketID:               pkt.GetPacketId(),
		Fixes:                  deduped,
		OriginalFixCount:       len(raw),
		InPacketDuplicateFixes: dupCount,
	}, nil
}

// Fingerprint computes the canonical SHA-256 fingerprint of the normalized
// packet: deterministic binary Protobuf encoding of the sorted, deduplicated
// packet, so JSON/binary input, fix order, explicit default scalars and
// repeated identical fix multiplicity never change packet identity (AG-04
// step 2).
func (p *NormalizedPacket) Fingerprint() ([32]byte, error) {
	canonical := &telemetryv1.TelemetryPacket{
		SimulatorId: p.SimulatorID,
		VehicleId:   p.VehicleID,
		PacketId:    p.PacketID,
	}
	for _, f := range p.Fixes {
		canonical.Fixes = append(canonical.Fixes, &telemetryv1.GPSFix{
			TimestampMs:    f.TimestampMs,
			Latitude:       f.Latitude,
			Longitude:      f.Longitude,
			SpeedMps:       f.SpeedMps,
			HeadingDegrees: f.HeadingDegrees,
		})
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(canonical)
	if err != nil {
		return [32]byte{}, fmt.Errorf("httpapi: marshal canonical packet: %w", err)
	}
	return sha256.Sum256(data), nil
}
