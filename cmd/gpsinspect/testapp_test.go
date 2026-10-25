package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"gpsdemo/internal/inspectclient"
	"gpsdemo/internal/inspector"
)

// testAppOption configures newTestApp.
type testAppOption func(*testAppConfig)

type testAppConfig struct {
	stdinTerminal  bool
	stderrTerminal bool
	stdin          string
}

func withTerminals(stdin, stderr bool) testAppOption {
	return func(c *testAppConfig) { c.stdinTerminal = stdin; c.stderrTerminal = stderr }
}

func withStdin(s string) testAppOption {
	return func(c *testAppConfig) { c.stdin = s }
}

// newTestApp constructs an *inspector.App directly (bypassing the Cobra
// command tree and os.File-based terminal detection), so tests can
// precisely control StdinIsTerminal/StderrIsTerminal without a real pty,
// per INS-01's "Tests must not require an interactive terminal."
func newTestApp(t *testing.T, baseURL string, opts ...testAppOption) (*inspector.App, context.Context) {
	t.Helper()
	cfg := testAppConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	u, err := inspectclient.ValidateBaseURL(baseURL)
	if err != nil {
		t.Fatalf("invalid test base URL: %v", err)
	}

	client := &inspectclient.Client{
		BaseURL:          u,
		HTTPClient:       newHTTPClient(),
		MaxResponseBytes: 16 << 20,
		Timeout:          5 * time.Second,
	}

	app := &inspector.App{
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader(cfg.stdin),
		Client: client, Config: inspector.DefaultConfig(),
		Now:              time.Now,
		StdinIsTerminal:  cfg.stdinTerminal,
		StderrIsTerminal: cfg.stderrTerminal,
	}
	return app, context.Background()
}

// runCloseDirect invokes inspector.RunClose directly, for tests that need
// precise control over terminal/stdin simulation (see newTestApp).
func runCloseDirect(ctx context.Context, app *inspector.App, tripID string, force bool) (*inspector.CloseReport, error) {
	return inspector.RunClose(ctx, app, tripID, inspector.CloseParams{Force: force})
}
