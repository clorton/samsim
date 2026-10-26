package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"gpsdemo/internal/apierr"
	"gpsdemo/internal/control"
)

// handleListSimulators implements GET /api/v1/simulators (AG-09): returns
// configured IDs and hints without blocking on any upstream call.
func (s *Server) handleListSimulators(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.Control.List()})
}

// handleSimulatorStatus implements GET /api/v1/simulators/{id}/status (AG-09).
func (s *Server) handleSimulatorStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	body, err := s.Control.Status(r.Context(), r.PathValue("id"))
	s.writeUpstreamResult(w, r, body, err)
}

// handleSimulatorConfig implements PUT /api/v1/simulators/{id}/config (AG-09).
func (s *Server) handleSimulatorConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, r, http.MethodPut)
		return
	}
	limited := http.MaxBytesReader(w, r.Body, s.MaxBodyBytes)
	reqBody, err := io.ReadAll(limited)
	if err != nil {
		writeError(w, r, apierr.New(apierr.CodeBodyTooLarge, "request body too large"))
		return
	}
	body, uerr := s.Control.Config(r.Context(), r.PathValue("id"), reqBody)
	s.writeUpstreamResult(w, r, body, uerr)
}

// handleSimulatorAction implements POST /api/v1/simulators/{id}/{action}
// (AG-09), restricted to the fixed allowlist.
func (s *Server) handleSimulatorAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	reqBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		writeError(w, r, apierr.New(apierr.CodeBodyTooLarge, "control request body too large"))
		return
	}
	body, err := s.Control.ActionBody(r.Context(), r.PathValue("id"), r.PathValue("action"), reqBody)
	s.writeUpstreamResult(w, r, body, err)
}

func (s *Server) handleSimulatorUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	if r.Header.Get("Content-Type") != "text/csv" {
		writeError(w, r, apierr.New(apierr.CodeUnsupportedMediaType, "upload requires text/csv"))
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		writeError(w, r, apierr.New(apierr.CodeBodyTooLarge, "CSV exceeds 64 MiB upload limit"))
		return
	}
	body, err := s.Control.UploadRoute(r.Context(), r.PathValue("id"), data)
	s.writeUpstreamResult(w, r, body, err)
}

func (s *Server) writeUpstreamResult(w http.ResponseWriter, r *http.Request, body []byte, err error) {
	if err != nil {
		switch {
		case errors.Is(err, control.ErrUnknownSimulator):
			writeError(w, r, apierr.Newf(apierr.CodeNotFound, "unknown simulator"))
		case errors.Is(err, control.ErrUnknownAction):
			writeError(w, r, apierr.Newf(apierr.CodeNotFound, "unknown action"))
		default:
			var ue *control.UpstreamError
			if errors.As(err, &ue) {
				switch ue.Kind {
				case "timeout":
					writeError(w, r, apierr.New(apierr.CodeSimulatorTimeout, "simulator did not respond in time"))
				case "unavailable":
					writeError(w, r, apierr.New(apierr.CodeSimulatorUnavailable, "simulator is unavailable"))
				case "client_error":
					// Pass through upstream validation 4xx using our
					// normalized error envelope where possible.
					var upstreamEnv struct {
						Error struct {
							Code    string         `json:"code"`
							Message string         `json:"message"`
							Details map[string]any `json:"details"`
						} `json:"error"`
					}
					if jsonErr := json.Unmarshal(ue.Body, &upstreamEnv); jsonErr == nil && upstreamEnv.Error.Code != "" {
						writeJSON(w, ue.StatusCode, upstreamEnv)
					} else {
						writeError(w, r, apierr.Newf(apierr.CodeInvalidRequest, "simulator rejected request (status %d)", ue.StatusCode))
					}
				}
				return
			}
			writeError(w, r, err)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}
