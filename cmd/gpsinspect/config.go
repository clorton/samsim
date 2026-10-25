package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"gpsdemo/internal/inspectclient"
	"gpsdemo/internal/inspector"
)

// resolveConfigInput bundles every global flag's raw value plus (for
// numeric flags that have a meaningful zero value) whether it was actually
// passed on the command line, so passing e.g. --page-size 0 is correctly
// rejected as invalid rather than silently treated as "not set" and falling
// back to the default.
type resolveConfigInput struct {
	base inspector.Config

	baseURL string
	timeout string

	jsonOutput bool
	noColor    bool

	pageSize    int
	pageSizeSet bool

	revisionAttempts    int
	revisionAttemptsSet bool

	maxFixes    int
	maxFixesSet bool

	maxResponseBytes    int64
	maxResponseBytesSet bool
}

// resolveConfig applies INS-01's precedence (explicit flag > environment >
// default) for every global flag, validating ranges before any network IO.
func resolveConfig(in resolveConfigInput) (inspector.Config, error) {
	cfg := in.base

	if in.baseURL != "" {
		cfg.BaseURL = in.baseURL
	} else if v := os.Getenv("GPSINSPECT_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}

	if in.timeout != "" {
		d, err := time.ParseDuration(in.timeout)
		if err != nil {
			return cfg, fmt.Errorf("invalid --timeout %q: %w", in.timeout, err)
		}
		cfg.Timeout = d
	} else if v := os.Getenv("GPSINSPECT_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid GPSINSPECT_TIMEOUT %q: %w", v, err)
		}
		cfg.Timeout = d
	}
	if cfg.Timeout <= 0 {
		return cfg, fmt.Errorf("--timeout must be positive")
	}

	cfg.JSON = in.jsonOutput
	cfg.NoColor = in.noColor

	if in.pageSizeSet {
		cfg.PageSize = in.pageSize
	}
	if cfg.PageSize < 1 || cfg.PageSize > 1000 {
		return cfg, fmt.Errorf("--page-size must be between 1 and 1000, got %d", cfg.PageSize)
	}

	if in.revisionAttemptsSet {
		cfg.RevisionAttempts = in.revisionAttempts
	}
	if cfg.RevisionAttempts < 1 || cfg.RevisionAttempts > 10 {
		return cfg, fmt.Errorf("--revision-attempts must be between 1 and 10, got %d", cfg.RevisionAttempts)
	}

	if in.maxFixesSet {
		cfg.MaxFixes = in.maxFixes
	}
	if cfg.MaxFixes < 1 {
		return cfg, fmt.Errorf("--max-fixes must be positive, got %d", cfg.MaxFixes)
	}

	if in.maxResponseBytesSet {
		cfg.MaxResponseBytes = in.maxResponseBytes
	}
	if cfg.MaxResponseBytes < 1 {
		return cfg, fmt.Errorf("--max-response-bytes must be positive, got %d", cfg.MaxResponseBytes)
	}

	return cfg, nil
}

// newHTTPClient builds an *http.Client that never follows redirects. The
// per-request timeout (INS-01: "5s per HTTP request/WS connection
// handshake", not a total command duration) is applied by
// inspectclient.Client itself via context.WithTimeout per request, not a
// blanket http.Client.Timeout, so a long-lived WebSocket upgrade (watch) is
// never killed by it.
func newHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: inspectclient.NoRedirect,
	}
}
