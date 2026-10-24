// Package apierr defines the aggregator's standard error shape and codes
// (AG-07), shared by HTTP handlers and the services that produce domain
// errors.
package apierr

import "fmt"

// Error codes (AG-07, AG-02, AG-04, AG-06).
const (
	CodeInvalidRequest        = "INVALID_REQUEST"
	CodeInvalidTelemetry      = "INVALID_TELEMETRY"
	CodeNotFound              = "NOT_FOUND"
	CodePacketConflict        = "PACKET_CONFLICT"
	CodeFixConflict           = "FIX_CONFLICT"
	CodeRevisionConflict      = "REVISION_CONFLICT"
	CodeTripIncomplete        = "TRIP_INCOMPLETE"
	CodeTripSuperseded        = "TRIP_SUPERSEDED"
	CodeBodyTooLarge          = "BODY_TOO_LARGE"
	CodeSnapshotTooLarge      = "SNAPSHOT_TOO_LARGE"
	CodeUnsupportedMediaType  = "UNSUPPORTED_MEDIA_TYPE"
	CodeUnsupportedContentEnc = "UNSUPPORTED_CONTENT_ENCODING"
	CodeStorageUnavailable    = "STORAGE_UNAVAILABLE"
	CodeMethodNotAllowed      = "METHOD_NOT_ALLOWED"
	CodeSimulatorUnavailable  = "SIMULATOR_UNAVAILABLE"
	CodeSimulatorTimeout      = "SIMULATOR_TIMEOUT"
)

// HTTPStatus maps each code to its required HTTP status (AG-07).
var HTTPStatus = map[string]int{
	CodeInvalidRequest:        400,
	CodeInvalidTelemetry:      400,
	CodeNotFound:              404,
	CodePacketConflict:        409,
	CodeFixConflict:           409,
	CodeRevisionConflict:      409,
	CodeTripIncomplete:        409,
	CodeTripSuperseded:        409,
	CodeBodyTooLarge:          413,
	CodeSnapshotTooLarge:      413,
	CodeUnsupportedMediaType:  415,
	CodeUnsupportedContentEnc: 415,
	CodeStorageUnavailable:    503,
	CodeMethodNotAllowed:      405,
	CodeSimulatorUnavailable:  502,
	CodeSimulatorTimeout:      504,
}

// Error is a structured API error carrying a stable code, human message, and
// optional machine-readable details, rendered under the standard
// {"error":{...}} envelope.
type Error struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// New constructs an Error with no details.
func New(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Newf constructs an Error with a formatted message.
func Newf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithDetails returns a copy of e with Details set.
func (e *Error) WithDetails(details map[string]any) *Error {
	return &Error{Code: e.Code, Message: e.Message, Details: details}
}

// Status returns the HTTP status code for e, defaulting to 500 if the code
// is unrecognized (should not happen for well-formed Errors).
func (e *Error) Status() int {
	if s, ok := HTTPStatus[e.Code]; ok {
		return s
	}
	return 500
}
