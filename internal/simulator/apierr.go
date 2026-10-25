// This file defines the simulator control API's standard error shape
// (SIM-06), independent of the aggregator's internal/apierr package (the
// simulator must have no dependency on aggregator-specific code).
package simulator

import "fmt"

// Error codes (SIM-06).
const (
	CodeInvalidRequest          = "INVALID_REQUEST"
	CodeInvalidConfig           = "INVALID_CONFIG"
	CodeInvalidRoute            = "INVALID_ROUTE"
	CodeNotFound                = "NOT_FOUND"
	CodeUnknownRoute            = "UNKNOWN_ROUTE"
	CodeResetRequired           = "RESET_REQUIRED"
	CodeControlRevisionConflict = "CONTROL_REVISION_CONFLICT"
	CodeRetryConfirmationReq    = "RETRY_CONFIRMATION_REQUIRED"
	CodeStopRequired            = "STOP_REQUIRED"
	CodeSendInFlight            = "SEND_IN_FLIGHT"
	CodePendingData             = "PENDING_DATA"
	CodeBodyTooLarge            = "BODY_TOO_LARGE"
	CodeRouteTooLarge           = "ROUTE_TOO_LARGE"
	CodeUnsupportedMediaType    = "UNSUPPORTED_MEDIA_TYPE"
	CodeIdentityStoreUnavail    = "IDENTITY_STORE_UNAVAILABLE"
	CodeControlPrepTimeout      = "CONTROL_PREPARATION_TIMEOUT"
	CodeMethodNotAllowed        = "METHOD_NOT_ALLOWED"
)

// httpStatus maps each code to its required HTTP status (SIM-06).
var httpStatus = map[string]int{
	CodeInvalidRequest:          400,
	CodeInvalidConfig:           400,
	CodeInvalidRoute:            400,
	CodeNotFound:                404,
	CodeUnknownRoute:            404,
	CodeResetRequired:           409,
	CodeControlRevisionConflict: 409,
	CodeRetryConfirmationReq:    409,
	CodeStopRequired:            409,
	CodeSendInFlight:            409,
	CodePendingData:             409,
	CodeBodyTooLarge:            413,
	CodeRouteTooLarge:           413,
	CodeUnsupportedMediaType:    415,
	CodeIdentityStoreUnavail:    503,
	CodeControlPrepTimeout:      503,
	CodeMethodNotAllowed:        405,
}

// APIError is a structured simulator control-API error (SIM-06), rendered
// under the standard {"error":{...}} envelope.
type APIError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewAPIError constructs an APIError with no details.
func NewAPIError(code, message string) *APIError {
	return &APIError{Code: code, Message: message}
}

// NewAPIErrorf constructs an APIError with a formatted message.
func NewAPIErrorf(code, format string, args ...any) *APIError {
	return &APIError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithDetails returns a copy of e with Details set.
func (e *APIError) WithDetails(details map[string]any) *APIError {
	return &APIError{Code: e.Code, Message: e.Message, Details: details}
}

// Status returns the HTTP status code for e, defaulting to 500 if the code
// is unrecognized.
func (e *APIError) Status() int {
	if s, ok := httpStatus[e.Code]; ok {
		return s
	}
	return 500
}
