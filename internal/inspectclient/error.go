package inspectclient

import "fmt"

// ErrorKind classifies a client-observed failure so callers (the inspector
// command layer) can map it to the correct process exit code without
// re-parsing message text.
type ErrorKind string

const (
	// KindNetwork covers connection failures, DNS errors, and similar
	// transport-level problems (no HTTP response was obtained at all).
	KindNetwork ErrorKind = "network"
	// KindTimeout covers a request or WebSocket handshake that exceeded the
	// configured timeout.
	KindTimeout ErrorKind = "timeout"
	// KindProtocol covers a response that violated the documented contract:
	// malformed/truncated JSON, oversized body, inconsistent pagination, or
	// an identity/field that does not match what was requested.
	KindProtocol ErrorKind = "protocol"
	// KindAPI covers a well-formed {"error":{...}} envelope from the
	// aggregator (AG-07), carrying its code/message/details.
	KindAPI ErrorKind = "api"
	// KindInterrupted covers a request canceled by the caller's own context
	// (SIGINT during an in-flight request), distinct from KindTimeout so
	// callers can map it to the SIGINT exit code (130) rather than 1.
	KindInterrupted ErrorKind = "interrupted"
)

// Error is gpsinspect's typed client error, retaining AG-07 error codes and
// details rather than discarding them into a plain string (INS-01's "Retain
// AG-07 error codes/details in a typed client error").
type Error struct {
	Kind    ErrorKind
	Message string

	// Populated when Kind == KindAPI.
	Code    string
	Status  int
	Details map[string]any

	Err error // underlying error, if any (for %w unwrapping)
}

func (e *Error) Error() string {
	if e.Kind == KindAPI {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// IsCode reports whether e is an API error with the given code.
func IsCode(err error, code string) bool {
	var ce *Error
	if ae, ok := err.(*Error); ok {
		ce = ae
	} else {
		return false
	}
	return ce.Kind == KindAPI && ce.Code == code
}

func networkError(format string, args ...any) *Error {
	return &Error{Kind: KindNetwork, Message: fmt.Sprintf(format, args...)}
}

func timeoutError(format string, args ...any) *Error {
	return &Error{Kind: KindTimeout, Message: fmt.Sprintf(format, args...)}
}

func interruptedError(format string, args ...any) *Error {
	return &Error{Kind: KindInterrupted, Message: fmt.Sprintf(format, args...)}
}

func protocolError(format string, args ...any) *Error {
	return &Error{Kind: KindProtocol, Message: fmt.Sprintf(format, args...)}
}

func apiError(code, message string, status int, details map[string]any) *Error {
	return &Error{Kind: KindAPI, Code: code, Message: message, Status: status, Details: details}
}
