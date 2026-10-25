package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
)

func newVehiclesCmd(ctx context.Context, cc *cmdContext) *cobra.Command {
	var activeOnly bool
	cmd := &cobra.Command{
		Use:   "vehicles",
		Short: "List all known vehicles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := cc.app
			report, warnings, err := inspector.RunVehicles(ctx, a, inspector.VehiclesParams{ActiveOnly: activeOnly})
			if err != nil {
				return emitError(cc, a, "vehicles", err)
			}
			if a.Config.JSON {
				return inspectoutput.WriteDocument(a.Stdout, "vehicles", report, warnings)
			}
			for _, w := range warnings {
				fmt.Fprintln(a.Stderr, "Warning:", w)
			}
			if len(report.Items) == 0 {
				fmt.Fprintln(a.Stdout, "No vehicles found.")
				return nil
			}
			t := inspectoutput.NewTable(a.Stdout, "VEHICLE_ID", "LATEST_TIMESTAMP", "ACTIVE_TRIP", "REVISION")
			for _, v := range report.Items {
				t.Row(v.VehicleID, inspectoutput.FormatTimestampMsPtr(v.LatestTimestampMs), inspectoutput.StringOrDash(v.ActiveTripID), fmt.Sprintf("%d", v.Revision))
			}
			return t.Flush()
		},
	}
	cmd.Flags().BoolVar(&activeOnly, "active-only", false, "only show vehicles with a non-null active trip")
	return cmd
}
