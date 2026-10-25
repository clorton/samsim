package inspector

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"gpsdemo/internal/inspectclient"
)

// CloseOutcome enumerates the terminal states RunClose can report, which
// the command layer maps to process exit codes (INS-03).
type CloseOutcome string

const (
	CloseOutcomeClosed         CloseOutcome = "CLOSED"
	CloseOutcomeAlreadyClosed  CloseOutcome = "ALREADY_CLOSED"
	CloseOutcomeCanceled       CloseOutcome = "CANCELED"
	CloseOutcomeConfirmNeeded  CloseOutcome = "CONFIRMATION_REQUIRED"
	CloseOutcomeSuperseded     CloseOutcome = "TRIP_SUPERSEDED"
	CloseOutcomeRevisionStale  CloseOutcome = "REVISION_CONFLICT"
	CloseOutcomeIncompleteNew  CloseOutcome = "TRIP_INCOMPLETE"
	CloseOutcomeOutcomeUnknown CloseOutcome = "OUTCOME_UNKNOWN"
)

// CloseReport is the "close" command's data payload.
type CloseReport struct {
	Outcome            CloseOutcome                   `json:"outcome"`
	TripID             string                         `json:"trip_id"`
	Status             string                         `json:"status,omitempty"`
	CloseReason        string                         `json:"close_reason,omitempty"`
	Revision           int64                          `json:"revision,omitempty"`
	ForcedClose        bool                           `json:"forced_close,omitempty"`
	Completeness       *inspectclient.CompletenessDTO `json:"completeness,omitempty"`
	SupersededByTripID *string                        `json:"superseded_by_trip_id,omitempty"`
	Message            string                         `json:"message"`
}

// CloseParams holds the "close" command's options.
type CloseParams struct {
	Force bool
}

// CloseError wraps a terminal close outcome that should map to a nonzero
// exit code without being an *inspectclient.Error (e.g. user cancellation).
type CloseError struct {
	Report *CloseReport
	Exit   int
}

func (e *CloseError) Error() string { return e.Report.Message }

// RunClose implements `gpsinspect close <trip-id> [--force]` (INS-03). It
// performs preflight reads, confirmation gating, and at most one POST; see
// the step-by-step contract in INSPECTOR_SPEC.md section 6. promptConfirm is
// injected so tests can simulate stdin without a real terminal.
func RunClose(ctx context.Context, a *App, tripID string, p CloseParams) (*CloseReport, error) {
	// Step 1: preflight trip detail/completeness at one revision. Bounded
	// revision-attempt reads are allowed before presenting a prompt.
	detail, err := fetchCloseDetail(ctx, a, tripID)
	if err != nil {
		return nil, err
	}

	if detail.Status == inspectclient.TripStatusClosed && detail.CloseReason == inspectclient.CloseReasonManual {
		return &CloseReport{
			Outcome: CloseOutcomeAlreadyClosed, TripID: tripID,
			Status: detail.Status, CloseReason: detail.CloseReason, Revision: detail.UpdatedRevision,
			ForcedClose: detail.ForcedClose,
			Message:     "trip is already closed (MANUAL); no request sent",
		}, nil
	}
	if detail.Status == inspectclient.TripStatusClosed && detail.CloseReason == inspectclient.CloseReasonMerged {
		report := &CloseReport{
			Outcome: CloseOutcomeSuperseded, TripID: tripID,
			SupersededByTripID: detail.TerminalSuccessorTripID,
			Message:            "trip has been merged into a successor; the successor was not closed",
		}
		return report, &CloseError{Report: report, Exit: 1}
	}

	completeness := detail.Completeness
	hasGaps := completeness != nil && !completeness.Complete

	acknowledged := false
	if hasGaps {
		if p.Force {
			acknowledged = true
		} else if !a.Config.JSON && a.StdinIsTerminal && a.StderrIsTerminal {
			confirmed, interrupted := promptConfirm(ctx, a, tripID, completeness)
			if interrupted {
				report := &CloseReport{
					Outcome: CloseOutcomeCanceled, TripID: tripID,
					Message: "confirmation prompt interrupted; no request sent",
				}
				return report, &CloseError{Report: report, Exit: inspectoutputExitInterrupted}
			}
			if !confirmed {
				report := &CloseReport{
					Outcome: CloseOutcomeCanceled, TripID: tripID,
					Message: "closure canceled: not confirmed",
				}
				return report, &CloseError{Report: report, Exit: 4}
			}
			acknowledged = true
		} else {
			report := &CloseReport{
				Outcome: CloseOutcomeConfirmNeeded, TripID: tripID,
				Completeness: completeness,
				Message:      "trip has missing internal fixes; rerun with --force to acknowledge and close",
			}
			return report, &CloseError{Report: report, Exit: 4}
		}
	}

	// Step 4: submit the mutation exactly once.
	result, err := a.Client.CloseTrip(ctx, tripID, inspectclient.CloseRequest{
		ExpectedRevision: detail.pinnedRevision,
		Force:            acknowledged,
	})
	if err != nil {
		return handleCloseError(ctx, a, tripID, err)
	}

	return &CloseReport{
		Outcome: CloseOutcomeClosed, TripID: tripID,
		Status: result.Status, CloseReason: result.CloseReason, Revision: result.Revision,
		ForcedClose: result.ForcedClose, Completeness: result.Completeness,
		Message: "trip closed",
	}, nil
}

// inspectoutputExitInterrupted mirrors inspectoutput.ExitInterrupted without
// importing that package here (keeps inspector decoupled from the output
// formatting package); the command layer re-maps CloseError.Exit regardless.
const inspectoutputExitInterrupted = 130

// closeDetail bundles the preflight read with the exact revision used, so
// the POST always targets precisely what was reviewed.
type closeDetail struct {
	inspectclient.TripDetail
	pinnedRevision int64
}

func fetchCloseDetail(ctx context.Context, a *App, tripID string) (*closeDetail, error) {
	detail, _, r, err := inspectclient.RunCoherentForTrip(ctx, a.Client, tripID, a.Config.RevisionAttempts,
		func(ctx context.Context, d string, r int64, trip *inspectclient.TripDetail) (inspectclient.TripDetail, error) {
			return *trip, nil
		})
	if err != nil {
		return nil, err
	}
	return &closeDetail{TripDetail: detail, pinnedRevision: r}, nil
}

// promptConfirm prints the warning/summary and reads a line from stdin,
// per INS-03's exact prompt text and y/yes-only confirmation rule. EOF, a
// blank line, or any answer other than an exact case-insensitive "y"/"yes"
// is treated as "no" (cancel) via the first (bool) return; RunClose maps
// that to exit code 4. The read happens in its own goroutine so a canceled
// ctx (SIGINT) can be detected promptly even though bufio.Reader.ReadString
// does not itself respect context cancellation; in that case the second
// (interrupted) return is true and no confirmation is inferred either way
// ("Signal/EOF during a prompt sends nothing").
func promptConfirm(ctx context.Context, a *App, tripID string, c *inspectclient.CompletenessDTO) (confirmed bool, interrupted bool) {
	fmt.Fprintf(a.Stderr, "Warning: trip %s has %d missing internal fix(es) across %d interval(s). Boundary completeness is always unknown. Closure is immutable; late internal fixes will be quarantined.\n", tripID, c.MissingFixCount, len(c.MissingIntervals))
	fmt.Fprint(a.Stderr, "Close this incomplete trip permanently? [y/N] ")

	lineCh := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(a.Stdin)
		line, _ := reader.ReadString('\n') // EOF with a partial/empty line: treat as blank input (cancel)
		lineCh <- line
	}()

	select {
	case <-ctx.Done():
		return false, true
	case line := <-lineCh:
		answer := strings.ToLower(strings.TrimSpace(line))
		return answer == "y" || answer == "yes", false
	}
}

// handleCloseError interprets a failed close POST per step 5/6 of the spec.
func handleCloseError(ctx context.Context, a *App, tripID string, err error) (*CloseReport, error) {
	var ce *inspectclient.Error
	if ae, ok := err.(*inspectclient.Error); ok && ae.Kind == inspectclient.KindAPI {
		ce = ae
	}
	if ce == nil {
		// Network/timeout/protocol/interrupted: the response itself was lost
		// or unparseable, so the outcome is genuinely unknown -- the mutation
		// may or may not have landed server-side. Never retry blindly;
		// reconcile by rereading current trip state instead.
		return reconcileUnknownOutcome(ctx, a, tripID, err)
	}

	switch ce.Code {
	case "REVISION_CONFLICT":
		rev, _ := ce.Details["revision"]
		report := &CloseReport{
			Outcome: CloseOutcomeRevisionStale, TripID: tripID,
			Message: fmt.Sprintf("revision changed since review; current revision is %v. Re-run to review and confirm again.", rev),
		}
		return report, &CloseError{Report: report, Exit: 5}
	case "TRIP_SUPERSEDED":
		var successor *string
		if s, ok := ce.Details["successor_trip_id"].(string); ok {
			successor = &s
		}
		report := &CloseReport{
			Outcome: CloseOutcomeSuperseded, TripID: tripID,
			SupersededByTripID: successor,
			Message:            "trip was merged into a successor concurrently with this request; the successor was not closed",
		}
		return report, &CloseError{Report: report, Exit: 1}
	case "TRIP_INCOMPLETE":
		report := &CloseReport{
			Outcome: CloseOutcomeIncompleteNew, TripID: tripID,
			Message: "server reported a new incompleteness assessment during the write; review again rather than reposting automatically",
		}
		return report, &CloseError{Report: report, Exit: 4}
	default:
		return nil, err
	}
}

// reconcileUnknownOutcome implements step 6's reconciliation: if the POST's
// own response was lost (network/timeout/interrupted error, not an API
// error), query current trip detail once. If the trip is now CLOSED/MANUAL,
// report that a closure was observed (without claiming this client caused
// it); otherwise the outcome genuinely cannot be determined.
func reconcileUnknownOutcome(ctx context.Context, a *App, tripID string, causeErr error) (*CloseReport, error) {
	detail, derr := a.Client.GetTripDetail(ctx, tripID, nil)
	if derr != nil {
		report := &CloseReport{
			Outcome: CloseOutcomeOutcomeUnknown, TripID: tripID,
			Message: fmt.Sprintf("close request failed (%v) and outcome could not be reconciled (%v); inspect the trip manually before retrying", causeErr, derr),
		}
		return report, &CloseError{Report: report, Exit: 1}
	}
	if detail.Status == inspectclient.TripStatusClosed && detail.CloseReason == inspectclient.CloseReasonManual {
		report := &CloseReport{
			Outcome: CloseOutcomeAlreadyClosed, TripID: tripID,
			Status: detail.Status, CloseReason: detail.CloseReason, Revision: detail.UpdatedRevision,
			ForcedClose: detail.ForcedClose,
			Message:     "the original close request's response was lost, but the trip is now CLOSED/MANUAL; a closure was observed (this does not identify which client performed it)",
		}
		return report, nil
	}
	report := &CloseReport{
		Outcome: CloseOutcomeOutcomeUnknown, TripID: tripID,
		Message: fmt.Sprintf("close request failed (%v) and the trip is not CLOSED/MANUAL; outcome unknown, inspect/rerun manually", causeErr),
	}
	return report, &CloseError{Report: report, Exit: 1}
}
