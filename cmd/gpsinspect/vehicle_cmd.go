package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
)

func newVehicleCmd(ctx context.Context, cc *cmdContext) *cobra.Command {
	return &cobra.Command{
		Use:   "vehicle <vehicle-id>",
		Short: "Show one vehicle's latest state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := cc.app
			report, err := inspector.RunVehicle(ctx, a, args[0])
			if err != nil {
				return emitError(cc, a, "vehicle", err)
			}
			if a.Config.JSON {
				return inspectoutput.WriteDocument(a.Stdout, "vehicle", report, nil)
			}
			f := inspectoutput.NewFields(a.Stdout)
			f.Line("Vehicle ID", report.VehicleID)
			f.Line("Database ID", report.DatabaseID)
			f.Line("Revision", fmt.Sprintf("%d", report.Revision))
			f.Line("Active trip", inspectoutput.StringOrDash(report.ActiveTripID))
			f.Line("Trip gap (s)", fmt.Sprintf("%d", report.TripGapSeconds))
			f.Line("Expected fix interval (ms)", fmt.Sprintf("%d", report.ExpectedFixIntervalMs))
			f.Blank()
			if report.LatestFix == nil {
				f.Line("Latest fix", "(none)")
				return nil
			}
			lf := report.LatestFix
			f.Line("Latest fix timestamp", fmt.Sprintf("%s (%d)", inspectoutput.FormatTimestampMs(lf.TimestampMs), lf.TimestampMs))
			f.Line("Latest fix position", fmt.Sprintf("lat=%v lon=%v speed_mps=%v heading_deg=%v", lf.Latitude, lf.Longitude, lf.SpeedMps, lf.HeadingDegrees))
			f.Line("Latest fix current trip", inspectoutput.StringOrDash(lf.CurrentTripID))
			f.Line("Latest fix assignment reason", inspectoutput.StringOrDash(lf.AssignmentReason))
			return nil
		},
	}
}
