package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
	"gpsdemo/internal/inspectvalidate"
)

func newValidateCmd(ctx context.Context, cc *cmdContext) *cobra.Command {
	var baselinePath, writeBaselinePath string
	cmd := &cobra.Command{
		Use:   "validate <vehicle-id>",
		Short: "Independently validate reconstruction invariants (read-only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := cc.app

			var baseline *inspectvalidate.Baseline
			if baselinePath != "" {
				bl, err := inspectvalidate.LoadBaseline(baselinePath)
				if err != nil {
					*cc.exitCode = inspectoutput.ExitAPIOrIO
					return fmt.Errorf("load baseline: %w", err)
				}
				baseline = bl
			}

			obs, err := inspectvalidate.Collect(ctx, a.Client, args[0], inspectvalidate.CollectOptions{
				PageSize: a.Config.PageSize, RevisionAttempts: a.Config.RevisionAttempts, MaxFixes: a.Config.MaxFixes,
			})
			if err != nil {
				return emitError(cc, a, "validate", err)
			}

			if baseline != nil && baseline.DatabaseID != obs.DatabaseID {
				*cc.exitCode = inspectoutput.ExitAPIOrIO
				return fmt.Errorf("%w: baseline database_id %q does not match current database_id %q", inspectvalidate.ErrBaselineDatabaseMismatch, baseline.DatabaseID, obs.DatabaseID)
			}
			if baseline != nil && baseline.VehicleID != obs.VehicleID {
				*cc.exitCode = inspectoutput.ExitAPIOrIO
				return fmt.Errorf("baseline vehicle_id %q does not match requested vehicle %q", baseline.VehicleID, obs.VehicleID)
			}

			report := inspectvalidate.BuildReport(obs, baseline)

			if writeBaselinePath != "" && !report.HasFail() {
				if err := inspectvalidate.WriteBaselineExclusive(writeBaselinePath, inspectvalidate.ToBaseline(obs)); err != nil {
					*cc.exitCode = inspectoutput.ExitAPIOrIO
					return fmt.Errorf("write baseline: %w", err)
				}
			}

			if a.Config.JSON {
				if err := inspectoutput.WriteDocument(a.Stdout, "validate", report, nil); err != nil {
					return err
				}
			} else {
				printValidateReport(a, report)
			}

			if report.HasFail() {
				*cc.exitCode = inspectoutput.ExitValidationFailed
				return &commandError{err: fmt.Errorf("validation found %d FAIL check(s)", report.FailCount), exit: inspectoutput.ExitValidationFailed}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "compare against a baseline file from a prior run (enables V09/V10)")
	cmd.Flags().StringVar(&writeBaselinePath, "write-baseline", "", "write a new baseline file after a FAIL-free validation")
	return cmd
}

func printValidateReport(a *inspector.App, report *inspectvalidate.Report) {
	f := inspectoutput.NewFields(a.Stdout)
	f.Line("Database ID", report.DatabaseID)
	f.Line("Vehicle ID", report.VehicleID)
	f.Line("Revision", fmt.Sprintf("%d", report.Revision))
	f.Line("Overall", string(report.Overall))
	f.Line("Summary", fmt.Sprintf("PASS=%d FAIL=%d SKIP=%d WARN=%d", report.PassCount, report.FailCount, report.SkipCount, report.WarnCount))
	f.Blank()
	t := inspectoutput.NewTable(a.Stdout, "CHECK", "STATUS", "MESSAGE")
	for _, c := range report.Checks {
		t.Row(c.CheckID, string(c.Status), c.Message)
	}
	t.Flush()
	for _, c := range report.Checks {
		if c.Status == inspectvalidate.StatusFail {
			fmt.Fprintf(a.Stdout, "\n%s evidence (showing %d, %d total%s):\n", c.CheckID, len(c.Evidence), c.ViolationCount, truncatedSuffix(c.Truncated))
			for _, e := range c.Evidence {
				fmt.Fprintf(a.Stdout, "  - %s\n", e)
			}
		}
	}
	f.Blank()
	f.Line("Scope and limitations", "")
	for _, s := range report.Scope {
		fmt.Fprintf(a.Stdout, "  - %s\n", s)
	}
}

func truncatedSuffix(truncated bool) string {
	if truncated {
		return ", truncated"
	}
	return ""
}
