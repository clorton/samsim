// Command aggregator runs the GPS telemetry aggregator server (AG-01..10).
//
// Usage:
//
//	aggregator [-config path/to/config.yaml]
//
// With no -config flag, the built-in defaults from AGGREGATOR_SPEC.md AG-01
// are used (listen 127.0.0.1:8080, database ./data/gps.sqlite, 600s trip
// gap, 1000ms expected fix interval). See config.example.yaml for a
// documented example file.
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

	"gpsdemo/internal/aggregator"
	"gpsdemo/internal/clock"
	"gpsdemo/internal/config"
	"gpsdemo/internal/control"
	"gpsdemo/internal/events"
	"gpsdemo/internal/httpapi"
	"gpsdemo/internal/idgen"
	"gpsdemo/internal/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("aggregator exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to YAML config file (optional; built-in defaults otherwise)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	slog.Info("configuration loaded",
		"listen", cfg.Listen,
		"database_path", cfg.DatabasePath,
		"trip_gap_seconds", cfg.TripGapSeconds,
		"expected_fix_interval_ms", cfg.ExpectedFixIntervalMs,
		"max_body_bytes", cfg.MaxBodyBytes,
		"max_fixes_per_packet", cfg.MaxFixesPerPacket,
		"simulators_configured", len(cfg.Simulators),
	)

	if dir := filepath.Dir(cfg.DatabasePath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create database directory %q: %w", dir, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, storage.Options{
		Path:                  cfg.DatabasePath,
		BusyTimeoutMs:         cfg.SQLiteBusyTimeoutMs,
		TripGapSeconds:        cfg.TripGapSeconds,
		ExpectedFixIntervalMs: cfg.ExpectedFixIntervalMs,
	})
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()
	slog.Info("storage ready", "database_id", store.DatabaseID, "path", cfg.DatabasePath)

	hub := events.NewHub(cfg.WebSocketQueueCapacity)
	svc := aggregator.NewService(store, hub, clock.System{}, idgen.UUID{}, int(cfg.MaxFixesPerPacket))

	var proxy *control.Proxy
	if len(cfg.Simulators) > 0 {
		var endpoints []control.Endpoint
		for _, sim := range cfg.Simulators {
			endpoints = append(endpoints, control.Endpoint{
				SimulatorID: sim.SimulatorID,
				ControlURL:  sim.ControlURL,
				VehicleID:   sim.VehicleID,
			})
		}
		proxy = control.NewProxy(endpoints)
		slog.Info("simulator control proxy configured", "count", len(endpoints))
	}

	server := &httpapi.Server{
		Service:           svc,
		Hub:               hub,
		Control:           proxy,
		MaxBodyBytes:      cfg.MaxBodyBytes,
		MaxFixesPerPacket: int(cfg.MaxFixesPerPacket),
		SnapshotMaxBytes:  16 << 20,
	}

	httpServer := &http.Server{
		Addr:    cfg.Listen,
		Handler: server.Router(),
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("aggregator listening", "addr", cfg.Listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		slog.Info("shutdown signal received, stopping gracefully")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("aggregator stopped cleanly")
	return nil
}
