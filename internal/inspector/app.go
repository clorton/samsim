// Package inspector implements gpsinspect's command services: report
// construction, the close workflow, and shared app wiring (injected IO
// streams, clock, terminal detection, and HTTP client), per INS-01's
// testability requirement that "Tests must not require an interactive
// terminal or a running service unless labeled integration."
package inspector

import (
	"io"
	"time"

	"gpsdemo/internal/inspectclient"
)

// Config holds gpsinspect's global flag values (INS-01).
type Config struct {
	BaseURL          string
	Timeout          time.Duration
	JSON             bool
	PageSize         int
	RevisionAttempts int
	MaxFixes         int
	MaxResponseBytes int64
	NoColor          bool
}

// DefaultConfig returns INS-01's documented defaults.
func DefaultConfig() Config {
	return Config{
		BaseURL:          "http://127.0.0.1:8080",
		Timeout:          5 * time.Second,
		JSON:             false,
		PageSize:         100,
		RevisionAttempts: 3,
		MaxFixes:         1000000,
		MaxResponseBytes: 16777216,
		NoColor:          false,
	}
}

// App bundles everything a command needs: injected IO streams, the REST
// client, a clock, and terminal detection, so tests can run without a real
// terminal or network service.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	Client *inspectclient.Client
	Config Config

	Now func() time.Time

	// StdinIsTerminal/StderrIsTerminal are injected rather than detected via
	// os.File.Fd(), so tests can simulate either a terminal or non-terminal
	// in-process (INS-03's prompt gating depends on both).
	StdinIsTerminal  bool
	StderrIsTerminal bool
}
