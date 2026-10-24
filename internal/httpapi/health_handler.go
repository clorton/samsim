package httpapi

import "net/http"

// handleHealth implements GET /api/v1/health (AG-07).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	status, err := s.Service.Health(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"healthy": false,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, status)
}
