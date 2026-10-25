// Package inspectoutput implements gpsinspect's output contract (INS-01):
// the versioned JSON envelope for finite commands, NDJSON records for
// watch, human table/field formatting, and the documented process exit
// codes.
package inspectoutput

import (
	"encoding/json"
	"io"
)

// SchemaVersion is the current stable schema_version for all gpsinspect
// JSON/NDJSON output.
const SchemaVersion = 1

// Exit codes (INSPECTOR_SPEC.md section 3).
const (
	ExitOK                = 0
	ExitAPIOrIO           = 1
	ExitUsage             = 2
	ExitValidationFailed  = 3
	ExitCloseCanceled     = 4
	ExitRevisionExhausted = 5
	ExitInterrupted       = 130
)

// Document is the stable finite-command success envelope.
type Document struct {
	SchemaVersion int      `json:"schema_version"`
	Command       string   `json:"command"`
	Data          any      `json:"data"`
	Warnings      []string `json:"warnings"`
}

// ErrorDocument is the stable finite-command failure envelope.
type ErrorDocument struct {
	SchemaVersion int         `json:"schema_version"`
	Command       string      `json:"command"`
	Error         ErrorDetail `json:"error"`
}

// ErrorDetail is one error's code/message/details.
type ErrorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// WriteDocument writes a successful finite-command JSON document to w.
func WriteDocument(w io.Writer, command string, data any, warnings []string) error {
	if warnings == nil {
		warnings = []string{}
	}
	enc := json.NewEncoder(w)
	return enc.Encode(Document{SchemaVersion: SchemaVersion, Command: command, Data: data, Warnings: warnings})
}

// WriteErrorDocument writes a failed finite-command JSON document to w.
func WriteErrorDocument(w io.Writer, command, code, message string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	enc := json.NewEncoder(w)
	return enc.Encode(ErrorDocument{
		SchemaVersion: SchemaVersion, Command: command,
		Error: ErrorDetail{Code: code, Message: message, Details: details},
	})
}
