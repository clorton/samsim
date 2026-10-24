package httpapi

import (
	"net/http"

	"gpsdemo/internal/aggregator"
)

// handleListVehicles implements GET /api/v1/vehicles (AG-07).
func (s *Server) handleListVehicles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	limit, err := parseIntParam(r, "limit", 0)
	if err != nil {
		writeError(w, r, err)
		return
	}
	env, err := s.Service.ListVehicles(r.Context(), aggregator.ListVehiclesParams{
		ActiveOnly: parseBoolParam(r, "active_only"),
		Limit:      limit,
		Cursor:     r.URL.Query().Get("cursor"),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

// handleGetVehicle implements GET /api/v1/vehicles/{id} (AG-07).
func (s *Server) handleGetVehicle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	atRevision, err := parseInt64Param(r, "at_revision")
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.GetVehicle(r.Context(), r.PathValue("id"), atRevision)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleGetSnapshot implements GET /api/v1/vehicles/{id}/snapshot (AG-07).
func (s *Server) handleGetSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	snap, err := s.Service.GetSnapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleListTrips implements GET /api/v1/vehicles/{id}/trips (AG-07).
func (s *Server) handleListTrips(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	limit, err := parseIntParam(r, "limit", 0)
	if err != nil {
		writeError(w, r, err)
		return
	}
	atRevision, err := parseInt64Param(r, "at_revision")
	if err != nil {
		writeError(w, r, err)
		return
	}
	env, err := s.Service.ListTrips(r.Context(), r.PathValue("id"), aggregator.ListTripsParams{
		IncludeMerged: parseBoolParam(r, "include_merged"),
		Status:        r.URL.Query().Get("status"),
		Limit:         limit,
		Cursor:        r.URL.Query().Get("cursor"),
		AtRevision:    atRevision,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

// handleListVehicleFixes implements GET /api/v1/vehicles/{id}/fixes (AG-07).
func (s *Server) handleListVehicleFixes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	fromMs, err := parseInt64Param(r, "from_ms")
	if err != nil {
		writeError(w, r, err)
		return
	}
	toMs, err := parseInt64Param(r, "to_ms")
	if err != nil {
		writeError(w, r, err)
		return
	}
	limit, err := parseIntParam(r, "limit", 0)
	if err != nil {
		writeError(w, r, err)
		return
	}
	atRevision, err := parseInt64Param(r, "at_revision")
	if err != nil {
		writeError(w, r, err)
		return
	}
	env, err := s.Service.ListVehicleFixes(r.Context(), r.PathValue("id"), aggregator.ListFixesParams{
		FromMs:     fromMs,
		ToMs:       toMs,
		Assignment: r.URL.Query().Get("assignment"),
		Limit:      limit,
		Cursor:     r.URL.Query().Get("cursor"),
		AtRevision: atRevision,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}
