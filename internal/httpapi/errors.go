// Package httpapi wires the aggregator's domain service
// (internal/aggregator) to net/http: request parsing, response rendering,
// the standard error envelope (AG-07), and the WebSocket event endpoint
// (AG-08).
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"gpsdemo/internal/apierr"
)

// errorEnvelope is the standard {"error":{...}} response shape (AG-07).
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// writeError renders err using the standard envelope and status code. Any
// error type is accepted; non-*apierr.Error values are treated as internal
// (500) and their details are not leaked to the client.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	ae, ok := err.(*apierr.Error)
	if !ok {
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
	writeError(w, r, apierr.New(apierr.CodeMethodNotAllowed, "method not allowed"))
}
