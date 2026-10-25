package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
)

func newGapsCmd(ctx context.Context, cc *cmdContext) *cobra.Command {
	var intervalLimit int
	cmd := &cobra.Command{
		Use:   "gaps <trip-id>",
		Short: "Show a trip's internal completeness gaps",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := cc.app
			if intervalLimit < 0 {
				*cc.exitCode = inspectoutput.ExitUsage
				return fmt.Errorf("--interval-limit must be positive")
			}
			report, err := inspector.RunGaps(ctx, a, args[0], inspector.GapsParams{IntervalLimit: intervalLimit})
			if err != nil {
				return emitError(cc, a, "gaps", err)
			}
			if a.Config.JSON {
				return inspectoutput.WriteDocument(a.Stdout, "gaps", report, nil)
			}
			f := inspectoutput.NewFields(a.Stdout)
			f.Line("Trip ID", report.TripID)
			f.Line("Expected fix interval (ms)", fmt.Sprintf("%d", report.ExpectedFixIntervalMs))
			f.Line("Missing fix count", fmt.Sprintf("%d", report.MissingFixCount))
			if report.Complete {
				f.Line("Completeness", "No detected internal gaps; boundary completeness unknown")
			} else {
				f.Line("Completeness", report.Assessment)
			}
			f.Line("Total intervals", fmt.Sprintf("%d", report.TotalIntervals))
			f.Blank()
			if len(report.DisplayedIntervals) == 0 {
				fmt.Fprintln(a.Stdout, "No missing intervals.")
				return nil
			}
			t := inspectoutput.NewTable(a.Stdout, "START", "END", "MISSING_COUNT")
			for _, iv := range report.DisplayedIntervals {
				t.Row(inspectoutput.FormatTimestampMs(iv.StartMs), inspectoutput.FormatTimestampMs(iv.EndMs), fmt.Sprintf("%d", iv.MissingCount))
			}
			if err := t.Flush(); err != nil {
				return err
			}
			if report.Truncated {
				fmt.Fprintf(a.Stdout, "(showing %d of %d intervals; counts above are authoritative)\n", len(report.DisplayedIntervals), report.TotalIntervals)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&intervalLimit, "interval-limit", 0, "max intervals to display, 1-1000 (default 20)")
	return cmd
}
