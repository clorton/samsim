package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"gpsdemo/internal/apierr"

	telemetryv1 "gpsdemo/gen/telemetry/v1"
)

// ContentTypeJSON and ContentTypeProtobuf are the two supported telemetry
// ingestion media types (AG-02).
const (
	ContentTypeJSON     = "application/json"
	ContentTypeProtobuf = "application/x-protobuf"
)

func newDecodeErr(code, format string, args ...any) *apierr.Error {
	return apierr.Newf(code, format, args...)
}

// DecodeJSONPacket strictly decodes a JSON telemetry packet: rejects unknown
// fields, duplicate keys, trailing documents, and malformed JSON (AG-02).
func DecodeJSONPacket(body []byte) (*telemetryv1.TelemetryPacket, error) {
	if err := checkNoDuplicateKeys(body); err != nil {
		return nil, newDecodeErr("INVALID_REQUEST", "malformed JSON: %v", err)
	}
	if err := checkNoTrailingData(body); err != nil {
		return nil, newDecodeErr("INVALID_REQUEST", "malformed JSON: %v", err)
	}

	var pkt telemetryv1.TelemetryPacket
	opts := protojson.UnmarshalOptions{DiscardUnknown: false}
	if err := opts.Unmarshal(body, &pkt); err != nil {
		return nil, newDecodeErr("INVALID_REQUEST", "invalid telemetry JSON: %v", err)
	}
	return &pkt, nil
}

// DecodeProtobufPacket strictly decodes a binary Protobuf telemetry packet,
// rejecting unknown fields anywhere in the message tree (AG-02).
func DecodeProtobufPacket(body []byte) (*telemetryv1.TelemetryPacket, error) {
	var pkt telemetryv1.TelemetryPacket
	if err := proto.Unmarshal(body, &pkt); err != nil {
		return nil, newDecodeErr("INVALID_REQUEST", "invalid telemetry protobuf: %v", err)
	}
	if len(pkt.ProtoReflect().GetUnknown()) > 0 {
		return nil, newDecodeErr("INVALID_REQUEST", "unknown top-level protobuf field")
	}
	for _, f := range pkt.GetFixes() {
		if len(f.ProtoReflect().GetUnknown()) > 0 {
			return nil, newDecodeErr("INVALID_REQUEST", "unknown protobuf field in GPSFix")
		}
	}
	return &pkt, nil
}

// checkNoDuplicateKeys walks the raw JSON token stream and rejects any
// object containing the same key twice at the same nesting level
// (prevalidation required before protojson decoding, AG-02).
func checkNoDuplicateKeys(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	type frame struct {
		keys map[string]bool
	}
	var stack []frame
	expectKey := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, frame{keys: map[string]bool{}})
				expectKey = true
			case '[':
				stack = append(stack, frame{keys: nil})
				expectKey = false
			case '}', ']':
				if len(stack) == 0 {
					return fmt.Errorf("unbalanced JSON structure")
				}
				stack = stack[:len(stack)-1]
				if len(stack) > 0 && stack[len(stack)-1].keys != nil {
					expectKey = true
				} else {
					expectKey = false
				}
			}
		case string:
			if len(stack) > 0 && stack[len(stack)-1].keys != nil && expectKey {
				key := v
				if stack[len(stack)-1].keys[key] {
					return fmt.Errorf("duplicate key %q", key)
				}
				stack[len(stack)-1].keys[key] = true
				expectKey = false
			} else if len(stack) > 0 && stack[len(stack)-1].keys != nil {
				// This was a value; the next string token at this level is a key.
				expectKey = true
			}
		default:
			if len(stack) > 0 && stack[len(stack)-1].keys != nil {
				expectKey = true
			}
		}
	}
	return nil
}

// checkNoTrailingData rejects any content after the first top-level JSON
// value, including additional whitespace-separated documents (AG-02).
func checkNoTrailingData(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing JSON content after packet document")
	}
	return nil
}

// ValidatePacketSemantics checks the decoded packet against AG-02's field
// constraints: ID formats, fix count bounds, coordinate/time/measurement
// ranges, and finiteness. It does not sort, dedupe, or fingerprint; call
// NormalizePacket for that.
func ValidatePacketSemantics(pkt *telemetryv1.TelemetryPacket, maxFixesPerPacket int) error {
	if err := ValidateEntityID("simulator_id", pkt.GetSimulatorId()); err != nil {
		return newDecodeErr("INVALID_TELEMETRY", "%v", err)
	}
	if err := ValidateEntityID("vehicle_id", pkt.GetVehicleId()); err != nil {
		return newDecodeErr("INVALID_TELEMETRY", "%v", err)
	}
	if err := ValidatePacketID(pkt.GetPacketId()); err != nil {
		return newDecodeErr("INVALID_TELEMETRY", "%v", err)
	}
	n := len(pkt.GetFixes())
	if n < 1 {
		return newDecodeErr("INVALID_TELEMETRY", "packet must contain at least one fix")
	}
	if n > maxFixesPerPacket {
		return newDecodeErr("INVALID_TELEMETRY", "packet contains %d fixes, exceeding max_fixes_per_packet=%d", n, maxFixesPerPacket)
	}
	const maxTimestampMs = 253402300799999 // end of year 9999, UTC
	for i, f := range pkt.GetFixes() {
		if f.GetTimestampMs() <= 0 {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: timestamp_ms must be > 0, got %d", i, f.GetTimestampMs())
		}
		if f.GetTimestampMs() > maxTimestampMs {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: timestamp_ms exceeds maximum %d", i, maxTimestampMs)
		}
		if math.IsNaN(f.GetLatitude()) || math.IsInf(f.GetLatitude(), 0) {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: latitude must be finite", i)
		}
		if f.GetLatitude() < -90 || f.GetLatitude() > 90 {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: latitude %v out of range [-90,90]", i, f.GetLatitude())
		}
		if math.IsNaN(f.GetLongitude()) || math.IsInf(f.GetLongitude(), 0) {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: longitude must be finite", i)
		}
		if f.GetLongitude() < -180 || f.GetLongitude() > 180 {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: longitude %v out of range [-180,180]", i, f.GetLongitude())
		}
		speed := float64(f.GetSpeedMps())
		if math.IsNaN(speed) || math.IsInf(speed, 0) {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: speed_mps must be finite", i)
		}
		if f.GetSpeedMps() < 0 {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: speed_mps must be >= 0, got %v", i, f.GetSpeedMps())
		}
		heading := float64(f.GetHeadingDegrees())
		if math.IsNaN(heading) || math.IsInf(heading, 0) {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: heading_degrees must be finite", i)
		}
		if f.GetHeadingDegrees() < 0 || f.GetHeadingDegrees() >= 360 {
			return newDecodeErr("INVALID_TELEMETRY", "fix[%d]: heading_degrees %v out of range [0,360)", i, f.GetHeadingDegrees())
		}
	}
	return nil
}
