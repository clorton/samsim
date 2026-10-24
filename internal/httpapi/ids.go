package httpapi

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// ValidateEntityID validates a simulator or vehicle ID: ASCII
// [A-Za-z0-9._-], 1-128 bytes (AG-02).
func ValidateEntityID(field, id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%s: must match [A-Za-z0-9._-]{1,128}, got %q", field, id)
	}
	return nil
}

var packetIDPattern = regexp.MustCompile(`^([0-9]{8})-([0-9]{1,20})$`)

// ValidatePacketID validates the packet_id format: a valid Gregorian UTC
// date YYYYMMDD, a literal hyphen, and 1-20 decimal digits (sequence),
// leading zeros preserved as part of identity (AG-02). The date describes
// packet creation, not fix timestamps.
func ValidatePacketID(id string) error {
	m := packetIDPattern.FindStringSubmatch(id)
	if m == nil {
		return fmt.Errorf("packet_id: must match YYYYMMDD-SEQUENCE (1-20 digits), got %q", id)
	}
	datePart := m[1]
	if _, err := time.Parse("20060102", datePart); err != nil {
		return fmt.Errorf("packet_id: %q is not a valid Gregorian UTC date: %w", datePart, err)
	}
	return nil
}

// SequencePart returns the decimal sequence suffix of a validated packet ID.
func SequencePart(packetID string) (string, error) {
	m := packetIDPattern.FindStringSubmatch(packetID)
	if m == nil {
		return "", fmt.Errorf("packet_id: invalid format %q", packetID)
	}
	return m[2], nil
}

// ParseSequencePart parses the numeric sequence suffix, mostly useful for
// simulators generating new IDs; aggregator only compares identity/content.
func ParseSequencePart(packetID string) (int64, error) {
	seq, err := SequencePart(packetID)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(seq, 10, 64)
}
