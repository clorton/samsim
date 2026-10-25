package simulator

import (
	"errors"
	"net/http"
)

// configUpdateBody is the JSON wire shape of PUT /config's request body
// (SIM-06). All fields are optional pointers; at least one must be set.
type configUpdateBody struct {
	ClockMultiplier              *float64 `json:"clock_multiplier,omitempty"`
	MaxPacketsPerSimulatedSecond *float64 `json:"max_packets_per_simulated_second,omitempty"`
	LiveWeight                   *int     `json:"live_weight,omitempty"`
	BacklogWeight                *int     `json:"backlog_weight,omitempty"`

	PacketSize       *int    `json:"packet_size,omitempty"`
	Encoding         *string `json:"encoding,omitempty"`
	RequestTimeoutMs *int64  `json:"request_timeout_ms,omitempty"`

	VehicleID             *string   `json:"vehicle_id,omitempty"`
	RouteID               *string   `json:"route_id,omitempty"`
	RunStartMs            *RunStart `json:"run_start_ms,omitempty"`
	AggregatorURL         *string   `json:"aggregator_url,omitempty"`
	ExpectedFixIntervalMs *int64    `json:"expected_fix_interval_ms,omitempty"`

	NormalReportIntervalSimulatedMs *int64 `json:"normal_report_interval_simulated_ms,omitempty"`

	ExpectedControlRevision *int64 `json:"expected_control_revision,omitempty"`
}

func (b configUpdateBody) toConfigUpdate() ConfigUpdate {
	return ConfigUpdate{
		ClockMultiplier:                 b.ClockMultiplier,
		MaxPacketsPerSimulatedSecond:    b.MaxPacketsPerSimulatedSecond,
		LiveWeight:                      b.LiveWeight,
		BacklogWeight:                   b.BacklogWeight,
		PacketSize:                      b.PacketSize,
		Encoding:                        b.Encoding,
		RequestTimeoutMs:                b.RequestTimeoutMs,
		VehicleID:                       b.VehicleID,
		RouteID:                         b.RouteID,
		RunStartMs:                      b.RunStartMs,
		AggregatorURL:                   b.AggregatorURL,
		ExpectedFixIntervalMs:           b.ExpectedFixIntervalMs,
		NormalReportIntervalSimulatedMs: b.NormalReportIntervalSimulatedMs,
		ExpectedControlRevision:         b.ExpectedControlRevision,
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, r, http.MethodPut)
		return
	}
	var body configUpdateBody
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}

	update := body.toConfigUpdate()
	if err := s.Engine.PutConfig(update); err != nil {
		writeError(w, r, mapConfigUpdateError(err))
		return
	}
	writeJSON(w, http.StatusOK, s.Engine.Status())
}

// mapConfigUpdateError translates PutConfig's sentinel/wrapped errors into
// the appropriate APIError code (SIM-06).
func mapConfigUpdateError(err error) error {
	switch {
	case errors.Is(err, ErrNoFieldsProvided):
		return NewAPIError(CodeInvalidRequest, err.Error())
	case errors.Is(err, ErrControlRevisionConflict):
		return NewAPIError(CodeControlRevisionConflict, err.Error())
	case errors.Is(err, ErrFieldRequiresNoPending), errors.Is(err, ErrFieldRequiresReadyEmpty), errors.Is(err, ErrFieldRequiresStopped):
		return NewAPIError(CodeInvalidConfig, err.Error())
	case errors.Is(err, ErrInvalidRoute):
		return NewAPIError(CodeInvalidRoute, err.Error())
	default:
		// Validation/route-loading errors from PutConfig are wrapped with
		// fmt.Errorf("...: %w", ...); without a more specific sentinel,
		// treat them as invalid config/route input (400), which covers
		// Config.Validate failures and route parse/validate failures
		// alike -- both are caller input problems, not server faults.
		return NewAPIErrorf(CodeInvalidConfig, "%v", err)
	}
}
