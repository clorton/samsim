// Package httpapi wires the aggregator's domain service
// (internal/aggregator) to net/http: request parsing, response rendering,
// the standard error envelope (AG-07), and the WebSocket event endpoint
// (AG-08).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

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
//
// If err is (or wraps) the client having gone away -- the request context
// was canceled, typically because the browser aborted an in-flight fetch or
// navigated away -- this is logged at DEBUG rather than ERROR: it is a
// routine consequence of a live-polling dashboard, not a server fault, and
// a reply is still attempted (harmlessly dropped if the connection is
// indeed gone) since synchronous cancellation detection here would be
// racy.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	ae, ok := err.(*apierr.Error)
	if !ok {
		if isClientGone(r.Context(), err) {
			slog.DebugContext(r.Context(), "request canceled by client", "error", err, "path", r.URL.Path)
		} else {
			slog.ErrorContext(r.Context(), "unhandled internal error", "error", err, "path", r.URL.Path)
		}
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

// isClientGone reports whether err reflects the client disconnecting or
// canceling the request, rather than a genuine server-side fault: the
// request's own context was canceled (browser aborted the fetch, or
// navigated away), or err itself is a context.Canceled (propagated up
// through a query run with the request's context, e.g. from
// storage.ReadTx).
func isClientGone(ctx context.Context, err error) bool {
	return ctx.Err() == context.Canceled || errors.Is(err, context.Canceled)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		if isWriteBrokenByClient(err) {
			slog.Debug("client disconnected before response was fully written", "error", err)
		} else {
			slog.Error("failed to encode JSON response", "error", err)
		}
	}
}

// isWriteBrokenByClient reports whether err from writing an HTTP response
// body reflects the client closing/resetting the connection before the
// server finished writing -- routine when a browser navigates away or
// aborts a fetch mid-response -- rather than a genuine server-side fault.
func isWriteBrokenByClient(err error) bool {
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer")
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, r, apierr.New(apierr.CodeMethodNotAllowed, "method not allowed"))
}
