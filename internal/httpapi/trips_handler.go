package httpapi

import (
	"encoding/json"
	"net/http"

	"gpsdemo/internal/apierr"
)

// handleGetTrip implements GET /api/v1/trips/{id} (AG-07).
func (s *Server) handleGetTrip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	atRevision, err := parseInt64Param(r, "at_revision")
	if err != nil {
		writeError(w, r, err)
		return
	}
	detail, err := s.Service.GetTripDetail(r.Context(), r.PathValue("id"), atRevision)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleGetTripFixes implements GET /api/v1/trips/{id}/fixes (AG-07).
func (s *Server) handleGetTripFixes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	atRevision, err := parseInt64Param(r, "at_revision")
	if err != nil {
		writeError(w, r, err)
		return
	}
	env, err := s.Service.GetTripFixes(r.Context(), r.PathValue("id"), atRevision)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

// handleGetCompleteness implements GET /api/v1/trips/{id}/completeness (AG-06/07).
func (s *Server) handleGetCompleteness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	dto, err := s.Service.GetCompleteness(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

type closeRequestBody struct {
	ExpectedRevision *int64 `json:"expected_revision"`
	Force            bool   `json:"force"`
}

// handleCloseTrip implements POST /api/v1/trips/{id}/close (AG-06).
func (s *Server) handleCloseTrip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	limited := http.MaxBytesReader(w, r.Body, s.MaxBodyBytes)
	var body closeRequestBody
	dec := json.NewDecoder(limited)
	if err := dec.Decode(&body); err != nil {
		writeError(w, r, apierr.Newf(apierr.CodeInvalidRequest, "malformed request body: %v", err))
		return
	}
	if body.ExpectedRevision == nil {
		writeError(w, r, apierr.New(apierr.CodeInvalidRequest, "expected_revision is required"))
		return
	}

	result, err := s.Service.Close(r.Context(), r.PathValue("id"), *body.ExpectedRevision, body.Force)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
