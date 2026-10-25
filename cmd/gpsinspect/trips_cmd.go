package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"gpsdemo/internal/inspectclient"
	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
)

func newTripsCmd(ctx context.Context, cc *cmdContext) *cobra.Command {
	var includeMerged bool
	var status string
	cmd := &cobra.Command{
		Use:   "trips <vehicle-id>",
		Short: "List a vehicle's current trip history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := cc.app
			if status != "" && status != inspectclient.TripStatusActive && status != inspectclient.TripStatusInactive && status != inspectclient.TripStatusClosed {
				*cc.exitCode = inspectoutput.ExitUsage
				return fmt.Errorf("--status must be one of ACTIVE, INACTIVE, CLOSED")
			}
			report, err := inspector.RunTrips(ctx, a, args[0], inspector.TripsParams{IncludeMerged: includeMerged, Status: status})
			if err != nil {
				return emitError(cc, a, "trips", err)
			}
			if a.Config.JSON {
				return inspectoutput.WriteDocument(a.Stdout, "trips", report, nil)
			}
			if len(report.Items) == 0 {
				fmt.Fprintln(a.Stdout, "No trips found.")
				return nil
			}
			t := inspectoutput.NewTable(a.Stdout, "TRIP_ID", "START", "END", "FIXES", "STATUS", "CLOSE_REASON", "SUCCESSOR")
			for _, tr := range report.Items {
				label := tr.Status
				if tr.CloseReason == inspectclient.CloseReasonMerged {
					label = "CLOSED/MERGED (audit)"
				}
				t.Row(tr.TripID, inspectoutput.FormatTimestampMs(tr.StartMs), inspectoutput.FormatTimestampMs(tr.EndMs),
					fmt.Sprintf("%d", tr.FixCount), label, tr.CloseReason, inspectoutput.StringOrDash(tr.SupersededByTripID))
			}
			return t.Flush()
		},
	}
	cmd.Flags().BoolVar(&includeMerged, "include-merged", false, "also include CLOSED/MERGED audit predecessors")
	cmd.Flags().StringVar(&status, "status", "", "filter by status: ACTIVE, INACTIVE, or CLOSED")
	return cmd
}
