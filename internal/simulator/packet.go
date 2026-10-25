// This file implements SIM-03's packet construction and encoding:
// selecting an ordered slice of RouteRecord values and serializing them
// once, immutably, into a Packet ready to send to the aggregator.
package simulator

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	telemetryv1 "gpsdemo/gen/telemetry/v1"
)

// Encoding identifies the wire encoding used for a packet.
type Encoding string

const (
	EncodingJSON     Encoding = "json"
	EncodingProtobuf Encoding = "protobuf"
)

// ContentType returns the HTTP Content-Type header value for e.
func (e Encoding) ContentType() string {
	switch e {
	case EncodingProtobuf:
		return "application/x-protobuf"
	default:
		return "application/json"
	}
}

// Packet is one immutable, already-encoded telemetry packet ready to send,
// per SIM-03: "Packet creation means selecting queue records for a send
// attempt... Serialize once... Retain immutable packet ID, message/body,
// encoding, selected records and lane until acknowledgment or explicit
// discard."
type Packet struct {
	PacketID    string
	SimulatorID string
	VehicleID   string
	Encoding    Encoding
	Body        []byte // the already-serialized request body (JSON or protobuf bytes)
	FixCount    int

	// FirstTimestampMs/LastTimestampMs are convenience accessors for the
	// packet's fix timestamp range (fixes are always ascending in event
	// time per spec).
	FirstTimestampMs int64
	LastTimestampMs  int64
}

// BuildPacket constructs and serializes an immutable Packet from an
// ordered (ascending timestamp) slice of RouteRecord values, a packet ID,
// simulator/vehicle IDs, and the desired encoding. It returns an error if
// fixes is empty, if timestamps are not strictly ascending (defensive
// check; this should never happen if the caller selects records
// correctly, but fail loudly rather than silently sending a possibly
// invalid packet), or if serialization fails.
func BuildPacket(packetID, simulatorID, vehicleID string, fixes []RouteRecord, encoding Encoding) (*Packet, error) {
	if len(fixes) == 0 {
		return nil, fmt.Errorf("packet: fixes must not be empty")
	}

	pbFixes := make([]*telemetryv1.GPSFix, len(fixes))
	for i, f := range fixes {
		if i > 0 && f.TimestampMs <= fixes[i-1].TimestampMs {
			return nil, fmt.Errorf("packet: fixes must be strictly ascending in timestamp_ms: fix %d has timestamp_ms %d, fix %d has timestamp_ms %d", i-1, fixes[i-1].TimestampMs, i, f.TimestampMs)
		}
		pbFixes[i] = &telemetryv1.GPSFix{
			TimestampMs:    f.TimestampMs,
			Latitude:       f.Latitude,
			Longitude:      f.Longitude,
			SpeedMps:       f.SpeedMps,
			HeadingDegrees: f.HeadingDegrees,
		}
	}

	pkt := &telemetryv1.TelemetryPacket{
		SimulatorId: simulatorID,
		VehicleId:   vehicleID,
		PacketId:    packetID,
		Fixes:       pbFixes,
	}

	var body []byte
	var err error
	switch encoding {
	case EncodingJSON:
		body, err = protojson.MarshalOptions{}.Marshal(pkt)
		if err != nil {
			return nil, fmt.Errorf("packet: protojson marshal: %w", err)
		}
	case EncodingProtobuf:
		body, err = proto.Marshal(pkt)
		if err != nil {
			return nil, fmt.Errorf("packet: proto marshal: %w", err)
		}
	default:
		return nil, fmt.Errorf("packet: unknown encoding %q", encoding)
	}

	return &Packet{
		PacketID:         packetID,
		SimulatorID:      simulatorID,
		VehicleID:        vehicleID,
		Encoding:         encoding,
		Body:             body,
		FixCount:         len(fixes),
		FirstTimestampMs: fixes[0].TimestampMs,
		LastTimestampMs:  fixes[len(fixes)-1].TimestampMs,
	}, nil
}
