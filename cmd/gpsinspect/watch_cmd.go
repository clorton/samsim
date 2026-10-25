package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
	"gpsdemo/internal/inspectwatch"
)

const defaultReconnectAttempts = 10

func newWatchCmd(ctx context.Context, cc *cmdContext) *cobra.Command {
	var reconnectAttempts int
	cmd := &cobra.Command{
		Use:   "watch <vehicle-id>",
		Short: "Watch live semantic events for a vehicle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := cc.app
			attempts := reconnectAttempts
			if !cmd.Flags().Changed("reconnect-attempts") {
				attempts = defaultReconnectAttempts
			}
			if attempts < 0 {
				*cc.exitCode = inspectoutput.ExitUsage
				return fmt.Errorf("--reconnect-attempts must be non-negative")
			}

			dialer := inspectwatch.NewDialer(a.Client.BaseURL, a.Config.MaxResponseBytes)
			session := inspectwatch.NewSession(inspectwatch.Options{
				Dialer:            dialer,
				RestClient:        a.Client,
				VehicleID:         args[0],
				ReconnectAttempts: attempts,
			})

			ndjson := inspectoutput.NewNDJSONWriter(a.Stdout)
			err := session.Run(ctx, func(ev inspectwatch.Event) {
				renderWatchEvent(a, ndjson, ev)
			})
			if err == context.Canceled || ctx.Err() == context.Canceled {
				*cc.exitCode = inspectoutput.ExitInterrupted
				return nil // SIGINT is a normal termination, not a reported error
			}
			if err != nil {
				*cc.exitCode = inspectoutput.ExitAPIOrIO
				return &commandError{err: err, exit: inspectoutput.ExitAPIOrIO}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&reconnectAttempts, "reconnect-attempts", defaultReconnectAttempts, "reconnect attempts after a disconnect (0 disables reconnect)")
	return cmd
}

func renderWatchEvent(a *inspector.App, ndjson *inspectoutput.NDJSONWriter, ev inspectwatch.Event) {
	if a.Config.JSON {
		renderWatchEventJSON(ndjson, ev)
		return
	}
	renderWatchEventHuman(a, ev)
}

func renderWatchEventJSON(ndjson *inspectoutput.NDJSONWriter, ev inspectwatch.Event) {
	switch ev.Kind {
	case inspectwatch.EventSnapshot:
		_ = ndjson.Write(inspectoutput.SnapshotRecord{
			SchemaVersion: inspectoutput.SchemaVersion, RecordType: inspectoutput.RecordSnapshot,
			DatabaseID: ev.DatabaseID, VehicleID: ev.VehicleID, Revision: ev.Revision, Data: ev.Snapshot,
		})
	case inspectwatch.EventEnvelope:
		_ = ndjson.Write(inspectoutput.EventRecord{
			SchemaVersion: inspectoutput.SchemaVersion, RecordType: inspectoutput.RecordEvent,
			DatabaseID: ev.DatabaseID, VehicleID: ev.VehicleID, Revision: ev.Revision, Data: ev.Envelope,
		})
	case inspectwatch.EventResync:
		_ = ndjson.Write(inspectoutput.ResyncRecord{
			SchemaVersion: inspectoutput.SchemaVersion, RecordType: inspectoutput.RecordResync, Reason: ev.Reason,
		})
	case inspectwatch.EventWaiting:
		_ = ndjson.Write(inspectoutput.WaitingRecord{
			SchemaVersion: inspectoutput.SchemaVersion, RecordType: inspectoutput.RecordWaiting,
			VehicleID: ev.VehicleID, Message: ev.Message,
		})
	case inspectwatch.EventDisconnected:
		_ = ndjson.Write(inspectoutput.DisconnectedRecord{
			SchemaVersion: inspectoutput.SchemaVersion, RecordType: inspectoutput.RecordDisconnected,
			Message: ev.Message, Attempt: ev.Attempt,
		})
	case inspectwatch.EventError:
		_ = ndjson.Write(inspectoutput.ErrorRecord{
			SchemaVersion: inspectoutput.SchemaVersion, RecordType: inspectoutput.RecordError, Message: ev.Message,
		})
	}
}

func renderWatchEventHuman(a *inspector.App, ev inspectwatch.Event) {
	switch ev.Kind {
	case inspectwatch.EventSnapshot:
		fmt.Fprintf(a.Stdout, "[snapshot] vehicle=%s revision=%d database=%s\n", ev.VehicleID, ev.Revision, ev.DatabaseID)
	case inspectwatch.EventEnvelope:
		fmt.Fprintf(a.Stdout, "[event] vehicle=%s revision=%d at=%s\n", ev.VehicleID, ev.Revision, inspectoutput.FormatTimestampMs(ev.Envelope.TimestampMs))
		for _, c := range ev.Envelope.Changes {
			line := fmt.Sprintf("  - %s", c.Type)
			switch {
			case c.Type == "trips.merged":
				line += fmt.Sprintf(" predecessors=%v successor=%s", c.PredecessorTripIDs, c.SuccessorTripID)
			case c.TripID != "":
				line += fmt.Sprintf(" trip=%s", c.TripID)
				if c.CloseReason != "" {
					line += fmt.Sprintf(" close_reason=%s", c.CloseReason)
				}
			case c.Type == "vehicle.updated":
				line += fmt.Sprintf(" active_trip=%s", inspectoutput.StringOrDash(c.ActiveTripID))
			}
			fmt.Fprintln(a.Stdout, line)
		}
	case inspectwatch.EventResync:
		fmt.Fprintf(a.Stderr, "Resyncing (%s): fetching a fresh snapshot.\n", ev.Reason)
	case inspectwatch.EventWaiting:
		fmt.Fprintf(a.Stderr, "Waiting: %s\n", ev.Message)
	case inspectwatch.EventDisconnected:
		fmt.Fprintf(a.Stderr, "Disconnected (attempt %d): %s\n", ev.Attempt, ev.Message)
	case inspectwatch.EventError:
		fmt.Fprintf(a.Stderr, "Error: %s\n", ev.Message)
	}
}
