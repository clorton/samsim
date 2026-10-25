package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
)

func newTripCmd(ctx context.Context, cc *cmdContext) *cobra.Command {
	return &cobra.Command{
		Use:   "trip <trip-id>",
		Short: "Show one trip's detail, lineage, and completeness",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := cc.app
			report, err := inspector.RunTrip(ctx, a, args[0])
			if err != nil {
				return emitError(cc, a, "trip", err)
			}
			if a.Config.JSON {
				return inspectoutput.WriteDocument(a.Stdout, "trip", report, nil)
			}
			d := report.Detail
			f := inspectoutput.NewFields(a.Stdout)
			f.Line("Trip ID", d.TripID)
			f.Line("Vehicle ID", d.VehicleID)
			f.Line("Database ID", report.DatabaseID)
			f.Line("Revision (vehicle)", fmt.Sprintf("%d", report.Revision))
			f.Line("Start", fmt.Sprintf("%s (%d)", inspectoutput.FormatTimestampMs(d.StartMs), d.StartMs))
			f.Line("End", fmt.Sprintf("%s (%d)", inspectoutput.FormatTimestampMs(d.EndMs), d.EndMs))
			f.Line("Fix count", fmt.Sprintf("%d", d.FixCount))
			f.Line("Bounds", fmt.Sprintf("lat[%v,%v] lon[%v,%v]", d.Bounds.MinLatitude, d.Bounds.MaxLatitude, d.Bounds.MinLongitude, d.Bounds.MaxLongitude))
			f.Line("Status", d.Status)
			f.Line("Close reason", d.CloseReason)
			f.Line("Forced close", fmt.Sprintf("%v", d.ForcedClose))
			f.Line("Closed at", inspectoutput.FormatTimestampMsPtr(d.ClosedAtMs))
			f.Line("Created revision", fmt.Sprintf("%d", d.CreatedRevision))
			f.Line("Updated revision", fmt.Sprintf("%d", d.UpdatedRevision))
			f.Blank()
			if len(d.ImmediatePredecessorTripIDs) == 0 {
				f.Line("Immediate predecessors", "(none)")
			} else {
				f.Line("Immediate predecessors", fmt.Sprintf("%v", d.ImmediatePredecessorTripIDs))
			}
			f.Line("Immediate successor", inspectoutput.StringOrDash(d.ImmediateSuccessorTripID))
			f.Line("Terminal successor", inspectoutput.StringOrDash(d.TerminalSuccessorTripID))
			f.Blank()
			if d.Completeness != nil {
				c := d.Completeness
				f.Line("Completeness", fmt.Sprintf("%s (missing_fix_count=%d, intervals=%d)", c.Assessment, c.MissingFixCount, len(c.MissingIntervals)))
				f.Line("Boundary completeness", c.BoundaryCompleteness+" (no detected internal gaps does not prove complete receipt)")
			}
			return nil
		},
	}
}
