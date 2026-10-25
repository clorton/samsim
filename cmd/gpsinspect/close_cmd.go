package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
)

func newCloseCmd(ctx context.Context, cc *cmdContext) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "close <trip-id>",
		Short: "Manually close a trip (mutating; see --force)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := cc.app
			report, err := inspector.RunClose(ctx, a, args[0], inspector.CloseParams{Force: force})
			if err != nil {
				if report != nil {
					return emitCloseError(cc, a, report, err)
				}
				return emitError(cc, a, "close", err)
			}
			if a.Config.JSON {
				return inspectoutput.WriteDocument(a.Stdout, "close", report, nil)
			}
			printCloseReport(a.Stdout, report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "explicitly acknowledge known missing internal fixes")
	return cmd
}

// emitCloseError handles the case where RunClose returned both a non-nil
// report and a non-nil error (an *inspector.CloseError carrying the
// specific exit code). These are documented outcomes (e.g.
// CONFIRMATION_REQUIRED, TRIP_SUPERSEDED), not opaque failures, so --json
// mode still writes the full report as the stdout data document (with only
// a short diagnostic on stderr); human mode treats this as an error and
// writes the report to stderr instead of stdout, per spec ("Human
// warnings/prompts/errors go to stderr").
func emitCloseError(cc *cmdContext, a *inspector.App, report *inspector.CloseReport, err error) error {
	code, message, _, exit := classifyError(err)
	if a.Config.JSON {
		_ = inspectoutput.WriteDocument(a.Stdout, "close", report, nil)
		fmt.Fprintf(a.Stderr, "%s: %s\n", code, message)
	} else {
		printCloseReport(a.Stderr, report)
	}
	*cc.exitCode = exit
	return &commandError{err: err, exit: exit}
}

func printCloseReport(w io.Writer, report *inspector.CloseReport) {
	f := inspectoutput.NewFields(w)
	f.Line("Trip ID", report.TripID)
	f.Line("Outcome", string(report.Outcome))
	if report.Status != "" {
		f.Line("Status", report.Status)
		f.Line("Close reason", report.CloseReason)
		f.Line("Revision", fmt.Sprintf("%d", report.Revision))
		f.Line("Forced close", fmt.Sprintf("%v", report.ForcedClose))
	}
	if report.SupersededByTripID != nil {
		f.Line("Superseded by", *report.SupersededByTripID)
	}
	f.Line("Message", report.Message)
}
