// Command simulator runs the GPS fix generator described in
// SIMULATOR_SPEC.md (SIM-01..07).
//
// Usage:
//
//	simulator --config simulator.json
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gpsdemo/internal/simulator"
)

func main() {
	if err := run(); err != nil {
		slog.Error("simulator exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to JSON config file (required)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	if *configPath == "" {
		return fmt.Errorf("missing required -config flag")
	}

	cfg, err := simulator.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	slog.Info("configuration loaded",
		"listen", cfg.Listen,
		"simulator_id", cfg.SimulatorID,
		"vehicle_id", cfg.VehicleID,
		"aggregator_url", cfg.AggregatorURL,
		"route_id", cfg.RouteID,
		"clock_multiplier", cfg.ClockMultiplier,
		"packet_size", cfg.PacketSize,
		"encoding", cfg.Encoding,
		"max_packets_per_second", cfg.MaxPacketsPerSecond,
	)

	if dir := filepath.Dir(cfg.IdentityDBPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create identity db directory %q: %w", dir, err)
		}
	}

	identity, err := simulator.OpenIdentityStore(cfg.IdentityDBPath)
	if err != nil {
		return fmt.Errorf("open identity store: %w", err)
	}
	defer identity.Close()
	slog.Info("identity store ready", "path", cfg.IdentityDBPath)

	routeLoader := func(routeID string) ([]byte, error) {
		path, ok := cfg.Routes[routeID]
		if !ok {
			return nil, fmt.Errorf("unknown route_id %q", routeID)
		}
		return os.ReadFile(path)
	}

	routeData, err := routeLoader(cfg.RouteID)
	if err != nil {
		return fmt.Errorf("load initial route %q: %w", cfg.RouteID, err)
	}

	engine, err := simulator.NewEngine(simulator.EngineOptions{
		Config:      cfg,
		Identity:    identity,
		RouteData:   routeData,
		RouteLoader: routeLoader,
	})
	if err != nil {
		return fmt.Errorf("construct engine: %w", err)
	}
	engine.Run()
	defer engine.Close()

	server := &simulator.Server{Engine: engine}
	httpServer := &http.Server{
		Addr:    cfg.Listen,
		Handler: server.Router(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("simulator listening", "addr", cfg.Listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	}

	// On first termination signal: stop generation/new attempts, allow the
	// one in-flight request to finish within its configured timeout plus a
	// small shutdown margin, and exit only if unsent count is zero. If
	// unsent remains, stay up with the control API active and warn.
	engine.Stop()

	shutdownMargin := time.Duration(cfg.RequestTimeoutMs)*time.Millisecond + 2*time.Second
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownMargin)
	defer cancel()
	waitForInFlightClear(shutdownCtx, engine)

	unsent := engine.Status().TotalUnsentFixes
	if unsent > 0 {
		slog.Warn("shutdown requested but unsent telemetry remains; control API staying active",
			"total_unsent_fixes", unsent,
			"hint", "send an explicit reset (with discard_pending if needed) or resume/drain, then terminate again",
		)
		// Wait for a second signal before forcibly exiting.
		ctx2, stop2 := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop2()
		<-ctx2.Done()
		slog.Warn("second termination signal received; forcibly exiting", "total_unsent_fixes", engine.Status().TotalUnsentFixes)
	}

	if err := httpServer.Shutdown(context.Background()); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	slog.Info("simulator stopped")
	return nil
}

// waitForInFlightClear polls until no send is in flight or ctx expires,
// whichever comes first.
func waitForInFlightClear(ctx context.Context, e *simulator.Engine) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !e.Status().InFlight {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
