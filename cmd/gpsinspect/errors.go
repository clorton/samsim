package main

import (
	"errors"
	"fmt"

	"gpsdemo/internal/inspectclient"
	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
)

// commandError wraps an error with its determined process exit code, so
// run's top-level RunE handling can return the exact code without
// re-inspecting the error.
type commandError struct {
	err  error
	exit int
}

func (e *commandError) Error() string { return e.err.Error() }
func (e *commandError) Unwrap() error { return e.err }

// emitError classifies err, writes the appropriate finite-command output
// (JSON error document to stdout + short diagnostic to stderr, or a plain
// human message to stderr), and returns a *commandError carrying the exit
// code the top-level run() should use.
func emitError(cc *cmdContext, a *inspector.App, command string, err error) error {
	code, message, details, exit := classifyError(err)

	if a.Config.JSON {
		_ = inspectoutput.WriteErrorDocument(a.Stdout, command, code, message, details)
		fmt.Fprintf(a.Stderr, "%s: %s\n", code, message)
	} else {
		fmt.Fprintf(a.Stderr, "Error [%s]: %s\n", code, message)
	}
	*cc.exitCode = exit
	return &commandError{err: err, exit: exit}
}

// classifyError maps an inspectclient/inspector error to a stable code,
// human message, structured details, and exit code (INSPECTOR_SPEC.md
// section 3's exit code table).
func classifyError(err error) (code, message string, details map[string]any, exit int) {
	if ae, ok := err.(*inspectclient.Error); ok {
		switch ae.Kind {
		case inspectclient.KindAPI:
			return ae.Code, ae.Message, ae.Details, inspectoutput.ExitAPIOrIO
		case inspectclient.KindInterrupted:
			return "INTERRUPTED", ae.Message, nil, inspectoutput.ExitInterrupted
		case inspectclient.KindTimeout:
			return "TIMEOUT", ae.Message, nil, inspectoutput.ExitAPIOrIO
		case inspectclient.KindProtocol:
			return "PROTOCOL_ERROR", ae.Message, nil, inspectoutput.ExitAPIOrIO
		default:
			return "NETWORK_ERROR", ae.Message, nil, inspectoutput.ExitAPIOrIO
		}
	}

	var exhausted *inspectclient.ErrRevisionAttemptsExhausted
	if errors.As(err, &exhausted) {
		return "REVISION_ATTEMPTS_EXHAUSTED", err.Error(), nil, inspectoutput.ExitRevisionExhausted
	}

	var closeErr *inspector.CloseError
	if errors.As(err, &closeErr) {
		return string(closeErr.Report.Outcome), closeErr.Report.Message, nil, closeErr.Exit
	}

	return "INTERNAL", err.Error(), nil, inspectoutput.ExitAPIOrIO
}
