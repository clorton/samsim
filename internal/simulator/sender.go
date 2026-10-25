// This file implements SIM-03's HTTP sender: posting one already-built
// Packet to the aggregator's telemetry endpoint and categorizing the
// result into a definitive success or a failed/uncertain send.
package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AcceptanceReceipt mirrors the aggregator's AG-04 ingestion response body,
// validated and parsed from a successful send.
type AcceptanceReceipt struct {
	SimulatorID      string
	PacketID         string
	DuplicatePacket  bool
	AcceptedFixes    int
	DuplicateFixes   int
	QuarantinedFixes int
	TripUpdates      int
	Revision         int64
}

// rawAcceptanceReceipt is the JSON wire shape of the aggregator's AG-04
// ingestion response body.
type rawAcceptanceReceipt struct {
	SimulatorID      string `json:"simulator_id"`
	PacketID         string `json:"packet_id"`
	DuplicatePacket  bool   `json:"duplicate_packet"`
	AcceptedFixes    int    `json:"accepted_fixes"`
	DuplicateFixes   int    `json:"duplicate_fixes"`
	QuarantinedFixes int    `json:"quarantined_fixes"`
	TripUpdates      int    `json:"trip_updates"`
	Revision         int64  `json:"revision"`
}

// Failure kind constants for SendOutcome.FailureKind.
const (
	FailureKindNetworkError     = "network_error"     // connection refused, DNS failure, etc. -- acceptance NOT uncertain (request never reached server, most likely)
	FailureKindTimeout          = "timeout"           // request exceeded its deadline -- acceptance IS uncertain (may have been committed server-side)
	FailureKindNon200           = "non_200_status"    // got an HTTP response, but not 200
	FailureKindMalformedReceipt = "malformed_receipt" // got 200 but the body didn't parse/validate
)

// SendOutcome is the categorized result of one send attempt, per SIM-03's
// "Any network error, timeout, non-200, or malformed/mismatched receipt is
// a failed/uncertain send."
type SendOutcome struct {
	// Receipt is non-nil only on a fully successful, validated acceptance.
	Receipt *AcceptanceReceipt

	// The following are populated only when Receipt is nil (a failed send):
	Failed              bool
	FailureKind         string // see constants above
	HTTPStatus          int    // 0 if no response was received (network error/timeout)
	AcceptanceUncertain bool   // true if the aggregator may have already committed despite us observing a failure
	ErrorMessage        string // safe, non-sensitive description for logs/status; never include full packet bodies
}

// maxReceiptBodyBytes bounds how much of a response body we will read, to
// avoid memory blowup from a misbehaving server.
const maxReceiptBodyBytes = 1 << 20 // 1 MiB

// Sender sends one already-built Packet to the aggregator's telemetry
// endpoint over HTTP and categorizes the result. One Sender instance is
// meant to be used for at most one in-flight request at a time (the
// broader simulator state machine enforces the "only one request in
// flight" rule described in SIM-03; this type itself has no such
// built-in single-flight guard -- it just performs one send when asked).
type Sender struct {
	client       *http.Client
	telemetryURL string
	timeout      time.Duration
}

// NewSender returns a Sender posting to telemetryURL (the full endpoint
// URL, e.g. "http://127.0.0.1:8080/api/v1/telemetry" -- already including
// the path) with requestTimeout applied per attempt. The returned client
// has redirects disabled.
func NewSender(telemetryURL string, requestTimeout time.Duration) *Sender {
	return &Sender{
		client: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		telemetryURL: telemetryURL,
		timeout:      requestTimeout,
	}
}

// Send performs one HTTP POST of pkt.Body to the aggregator and returns a
// categorized SendOutcome. It respects ctx for cancellation in addition to
// the Sender's own per-request timeout (whichever fires first). It never
// retries internally -- exactly one HTTP attempt per call.
func (s *Sender) Send(ctx context.Context, pkt *Packet) SendOutcome {
	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, s.telemetryURL, bytes.NewReader(pkt.Body))
	if err != nil {
		return SendOutcome{
			Failed:       true,
			FailureKind:  FailureKindNetworkError,
			ErrorMessage: "failed to construct request",
		}
	}
	req.Header.Set("Content-Type", pkt.Encoding.ContentType())

	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return SendOutcome{
				Failed:              true,
				FailureKind:         FailureKindTimeout,
				AcceptanceUncertain: true,
				ErrorMessage:        "request timed out",
			}
		}
		return SendOutcome{
			Failed:              true,
			FailureKind:         FailureKindNetworkError,
			AcceptanceUncertain: false,
			ErrorMessage:        "network error",
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		uncertain := resp.StatusCode == http.StatusConflict || resp.StatusCode >= 500
		// Drain the body (bounded) so the connection can be reused; errors
		// here are not relevant to categorization.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReceiptBodyBytes))
		return SendOutcome{
			Failed:              true,
			FailureKind:         FailureKindNon200,
			HTTPStatus:          resp.StatusCode,
			AcceptanceUncertain: uncertain,
			ErrorMessage:        fmt.Sprintf("unexpected status %d", resp.StatusCode),
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReceiptBodyBytes))
	if err != nil {
		return SendOutcome{
			Failed:              true,
			FailureKind:         FailureKindMalformedReceipt,
			HTTPStatus:          http.StatusOK,
			AcceptanceUncertain: true,
			ErrorMessage:        "failed to read response body",
		}
	}

	var raw rawAcceptanceReceipt
	if err := json.Unmarshal(body, &raw); err != nil {
		return SendOutcome{
			Failed:              true,
			FailureKind:         FailureKindMalformedReceipt,
			HTTPStatus:          http.StatusOK,
			AcceptanceUncertain: true,
			ErrorMessage:        "receipt did not parse as JSON",
		}
	}

	if raw.SimulatorID != pkt.SimulatorID {
		return SendOutcome{
			Failed:              true,
			FailureKind:         FailureKindMalformedReceipt,
			HTTPStatus:          http.StatusOK,
			AcceptanceUncertain: true,
			ErrorMessage:        "receipt simulator_id mismatch",
		}
	}
	if raw.PacketID != pkt.PacketID {
		return SendOutcome{
			Failed:              true,
			FailureKind:         FailureKindMalformedReceipt,
			HTTPStatus:          http.StatusOK,
			AcceptanceUncertain: true,
			ErrorMessage:        "receipt packet_id mismatch",
		}
	}
	if raw.AcceptedFixes < 0 || raw.DuplicateFixes < 0 || raw.QuarantinedFixes < 0 || raw.TripUpdates < 0 || raw.Revision < 0 {
		return SendOutcome{
			Failed:              true,
			FailureKind:         FailureKindMalformedReceipt,
			HTTPStatus:          http.StatusOK,
			AcceptanceUncertain: true,
			ErrorMessage:        "receipt contains negative counters",
		}
	}
	if !raw.DuplicatePacket && raw.AcceptedFixes+raw.DuplicateFixes != pkt.FixCount {
		return SendOutcome{
			Failed:              true,
			FailureKind:         FailureKindMalformedReceipt,
			HTTPStatus:          http.StatusOK,
			AcceptanceUncertain: true,
			ErrorMessage:        "receipt counters do not match packet fix count",
		}
	}

	return SendOutcome{
		Receipt: &AcceptanceReceipt{
			SimulatorID:      raw.SimulatorID,
			PacketID:         raw.PacketID,
			DuplicatePacket:  raw.DuplicatePacket,
			AcceptedFixes:    raw.AcceptedFixes,
			DuplicateFixes:   raw.DuplicateFixes,
			QuarantinedFixes: raw.QuarantinedFixes,
			TripUpdates:      raw.TripUpdates,
			Revision:         raw.Revision,
		},
	}
}
