package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"gpsdemo/internal/inspectclient"
	"gpsdemo/internal/inspector"
	"gpsdemo/internal/inspectoutput"
)

// buildVersion is the injected build version (INS-01 --version); overridden
// at build time with -ldflags "-X main.buildVersion=...".
var buildVersion = "dev"

// cmdContext bundles the shared state every subcommand needs to read after
// PersistentPreRunE has run: the constructed App and a place to stash a
// more specific exit code than Cobra's default.
type cmdContext struct {
	app      *inspector.App
	exitCode *int
}

// run builds and executes the Cobra command tree against args, writing to
// the given IO streams, and returns the process exit code. Keeping this
// separate from main lets tests invoke the whole CLI in-process without an
// interactive terminal or os.Exit (INS-01).
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	defaults := inspector.DefaultConfig()

	stdinFile, stdinIsFile := stdin.(*os.File)
	stderrFile, stderrIsFile := stderr.(*os.File)
	stdinIsTerminal := stdinIsFile && isatty.IsTerminal(stdinFile.Fd())
	stderrIsTerminal := stderrIsFile && isatty.IsTerminal(stderrFile.Fd())

	root := &cobra.Command{
		Use:           "gpsinspect",
		Short:         "Inspect and validate GPS telemetry aggregator state",
		Version:       buildVersion,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)

	var baseURLFlag, timeoutFlag string
	var jsonFlag, noColorFlag bool
	var pageSizeFlag, revisionAttemptsFlag, maxFixesFlag int
	var maxResponseBytesFlag int64

	root.PersistentFlags().StringVar(&baseURLFlag, "base-url", "", "aggregator base URL (env GPSINSPECT_BASE_URL; default http://127.0.0.1:8080)")
	root.PersistentFlags().StringVar(&timeoutFlag, "timeout", "", "per-request/handshake timeout (env GPSINSPECT_TIMEOUT; default 5s)")
	root.PersistentFlags().BoolVar(&jsonFlag, "json", false, "machine-readable JSON (finite commands) / NDJSON (watch) output")
	root.PersistentFlags().IntVar(&pageSizeFlag, "page-size", 0, "pagination page size, 1-1000 (default 100)")
	root.PersistentFlags().IntVar(&revisionAttemptsFlag, "revision-attempts", 0, "max coherent-read attempts, 1-10 (default 3)")
	root.PersistentFlags().IntVar(&maxFixesFlag, "max-fixes", 0, "max raw/geometry rows fetched per command (default 1000000)")
	root.PersistentFlags().Int64Var(&maxResponseBytesFlag, "max-response-bytes", 0, "max bytes per response/frame (default 16777216)")
	root.PersistentFlags().BoolVar(&noColorFlag, "no-color", false, "disable ANSI color output")

	exitCode := 0
	cc := &cmdContext{exitCode: &exitCode}

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		flags := cmd.Flags()
		resolved, err := resolveConfig(resolveConfigInput{
			base:                defaults,
			baseURL:             baseURLFlag,
			timeout:             timeoutFlag,
			jsonOutput:          jsonFlag,
			pageSize:            pageSizeFlag,
			pageSizeSet:         flags.Changed("page-size"),
			revisionAttempts:    revisionAttemptsFlag,
			revisionAttemptsSet: flags.Changed("revision-attempts"),
			maxFixes:            maxFixesFlag,
			maxFixesSet:         flags.Changed("max-fixes"),
			maxResponseBytes:    maxResponseBytesFlag,
			maxResponseBytesSet: flags.Changed("max-response-bytes"),
			noColor:             noColorFlag,
		})
		if err != nil {
			exitCode = inspectoutput.ExitUsage
			return err
		}
		u, err := inspectclient.ValidateBaseURL(resolved.BaseURL)
		if err != nil {
			exitCode = inspectoutput.ExitUsage
			return err
		}

		client := &inspectclient.Client{
			BaseURL:          u,
			HTTPClient:       newHTTPClient(),
			MaxResponseBytes: resolved.MaxResponseBytes,
			Timeout:          resolved.Timeout,
		}
		cc.app = &inspector.App{
			Stdout: stdout, Stderr: stderr, Stdin: stdin,
			Client: client, Config: resolved,
			Now:              time.Now,
			StdinIsTerminal:  stdinIsTerminal,
			StderrIsTerminal: stderrIsTerminal,
		}
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root.AddCommand(newVehiclesCmd(ctx, cc))
	root.AddCommand(newVehicleCmd(ctx, cc))
	root.AddCommand(newTripsCmd(ctx, cc))
	root.AddCommand(newTripCmd(ctx, cc))
	root.AddCommand(newGapsCmd(ctx, cc))
	root.AddCommand(newCloseCmd(ctx, cc))
	root.AddCommand(newWatchCmd(ctx, cc))
	root.AddCommand(newValidateCmd(ctx, cc))

	if err := root.Execute(); err != nil {
		if exitCode == 0 {
			exitCode = inspectoutput.ExitAPIOrIO
		}
		fmt.Fprintln(stderr, "Error:", err)
		return exitCode
	}
	return exitCode
}
