// This file implements SIM-06's control HTTP API: GET /status, PUT
// /config, and POST /start, /stop, /pause-transmission,
// /resume-transmission, /reset.
package simulator

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

// maxControlBodyBytes bounds request bodies for control endpoints (SIM-06).
const maxControlBodyBytes = 64 * 1024

// Server wires an *Engine to net/http.
type Server struct {
	Engine *Engine
}

// Router builds the complete net/http handler for the simulator's control
// API.
func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/config", s.handleConfig)
	mux.HandleFunc("/start", s.handleStart)
	mux.HandleFunc("/stop", s.handleStop)
	mux.HandleFunc("/pause-transmission", s.handlePauseTransmission)
	mux.HandleFunc("/resume-transmission", s.handleResumeTransmission)
	mux.HandleFunc("/reset", s.handleReset)
	return mux
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *APIError
	if !errors.As(err, &ae) {
		slog.ErrorContext(r.Context(), "unhandled internal error", "error", err, "path", r.URL.Path)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope{Error: errorBody{
			Code:    "INTERNAL",
			Message: "internal server error",
		}})
		return
	}
	writeJSON(w, ae.Status(), errorEnvelope{Error: errorBody{
		Code:    ae.Code,
		Message: ae.Message,
		Details: ae.Details,
	}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, r, NewAPIError(CodeMethodNotAllowed, "method not allowed"))
}

// decodeJSONBody strictly decodes a request body as JSON into v: rejects
// unknown fields, trailing documents, and bodies over maxControlBodyBytes
// (SIM-06). An empty body is treated as "{}" (leaving v at its zero
// value).
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil || mediaType != "application/json" {
			return NewAPIErrorf(CodeUnsupportedMediaType, "unsupported media type %q", ct)
		}
	}
	limited := http.MaxBytesReader(w, r.Body, maxControlBodyBytes)
	data, err := io.ReadAll(limited)
	if err != nil {
		return NewAPIErrorf(CodeBodyTooLarge, "request body exceeds %d bytes", maxControlBodyBytes)
	}
	if len(data) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return NewAPIErrorf(CodeInvalidRequest, "malformed request body: %v", err)
	}
	if dec.More() {
		return NewAPIErrorf(CodeInvalidRequest, "trailing content after JSON document")
	}
	return nil
}

func handleStatusCommon(w http.ResponseWriter, r *http.Request, e *Engine) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, e.Status())
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	handleStatusCommon(w, r, s.Engine)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	if err := s.Engine.Start(); err != nil {
		if errors.Is(err, ErrResetRequired) {
			writeError(w, r, NewAPIError(CodeResetRequired, err.Error()))
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Engine.Status())
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	s.Engine.Stop()
	writeJSON(w, http.StatusOK, s.Engine.Status())
}

func (s *Server) handlePauseTransmission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	s.Engine.PauseTransmission()
	writeJSON(w, http.StatusOK, s.Engine.Status())
}

type resumeTransmissionBody struct {
	RetryFailed bool `json:"retry_failed"`
}

func (s *Server) handleResumeTransmission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	var body resumeTransmissionBody
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	err := s.Engine.ResumeTransmission(ResumeTransmissionOptions{RetryFailed: body.RetryFailed})
	if err != nil {
		if errors.Is(err, ErrRetryConfirmationRequired) {
			st := s.Engine.Status()
			details := map[string]any{"status": st}
			if st.PendingPacketID != nil {
				details["pending_packet_id"] = *st.PendingPacketID
			}
			writeError(w, r, NewAPIError(CodeRetryConfirmationReq, err.Error()).WithDetails(details))
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Engine.Status())
}

type resetBody struct {
	DiscardPending bool `json:"discard_pending"`
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	var body resetBody
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.Engine.Reset(body.DiscardPending)
	if err != nil {
		switch {
		case errors.Is(err, ErrStopRequired):
			writeError(w, r, NewAPIError(CodeStopRequired, err.Error()))
		case errors.Is(err, ErrSendInFlight):
			writeError(w, r, NewAPIError(CodeSendInFlight, err.Error()))
		case errors.Is(err, ErrPendingData):
			writeError(w, r, NewAPIError(CodePendingData, err.Error()).WithDetails(map[string]any{
				"total_unsent_fixes": s.Engine.Status().TotalUnsentFixes,
			}))
		default:
			writeError(w, r, err)
		}
		return
	}
	resp := struct {
		Status              Status `json:"status"`
		DiscardedFixes      int64  `json:"discarded_fixes"`
		AcceptanceUncertain bool   `json:"acceptance_uncertain"`
	}{
		Status:              s.Engine.Status(),
		DiscardedFixes:      result.DiscardedFixes,
		AcceptanceUncertain: result.AcceptanceUncertain,
	}
	writeJSON(w, http.StatusOK, resp)
}
