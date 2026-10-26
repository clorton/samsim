package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"gpsdemo/internal/apierr"
)

// TestIsClientGone_CanceledRequestContext verifies that a request whose own
// context was canceled (the standard signal for a browser aborting an
// in-flight fetch or navigating away mid-request) is classified as a
// client-disconnect, not a server fault, regardless of the specific wrapped
// error message surfaced by the data layer (context.Canceled itself, or an
// unrelated-looking driver error like "transaction has already been
// committed or rolled back" that Go's database/sql produces after an
// automatic rollback-on-cancellation).
func TestIsClientGone_CanceledRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		err  error
	}{
		{"direct context.Canceled", context.Canceled},
		{"wrapped context.Canceled", fmt.Errorf("storage: list trip fixes: %w", context.Canceled)},
		{"unrelated driver error after cancellation", errors.New("sql: transaction has already been committed or rolled back")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !isClientGone(ctx, c.err) {
				t.Fatalf("expected isClientGone(canceled ctx, %v) = true", c.err)
			}
		})
	}
}

// TestIsClientGone_NotCanceled verifies a live request context with an
// ordinary internal error is NOT classified as client-gone, so genuine
// server faults still log at ERROR.
func TestIsClientGone_NotCanceled(t *testing.T) {
	if isClientGone(context.Background(), errors.New("boom")) {
		t.Fatal("expected a live context with an unrelated error to not be classified as client-gone")
	}
}

// TestIsWriteBrokenByClient recognizes the two concrete error strings
// observed in practice (broken pipe, connection reset by peer) alongside a
// generic *net.OpError, and does not misclassify an unrelated encoding
// error.
func TestIsWriteBrokenByClient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"broken pipe", errors.New("write tcp 127.0.0.1:8080->127.0.0.1:61564: write: broken pipe"), true},
		{"connection reset", errors.New("write tcp 127.0.0.1:8080->127.0.0.1:61564: write: connection reset by peer"), true},
		{"unrelated error", errors.New("json: unsupported type: chan int"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isWriteBrokenByClient(c.err); got != c.want {
				t.Fatalf("isWriteBrokenByClient(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestWriteError_CanceledContextLogsAtDebugNotError exercises writeError
// end-to-end: a request whose context is already canceled, given a
// non-*apierr.Error (the "unhandled internal error" path), must log at
// DEBUG rather than ERROR, so a live-polling dashboard's routine aborted
// fetches don't flood operator-facing logs. A genuine internal error on a
// live request must still log at ERROR.
func TestWriteError_CanceledContextLogsAtDebugNotError(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/api/v1/trips/x/fixes", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	writeError(rec, req, fmt.Errorf("storage: list trip fixes: %w", context.Canceled))

	out := buf.String()
	if strings.Contains(out, "level=ERROR") {
		t.Fatalf("expected no ERROR-level log for a client-canceled request, got log output: %s", out)
	}
	if !strings.Contains(out, "level=DEBUG") || !strings.Contains(out, "request canceled by client") {
		t.Fatalf("expected a DEBUG-level \"request canceled by client\" log, got: %s", out)
	}
	if rec.Code != 500 {
		t.Fatalf("expected the response to still be written (500 INTERNAL), got %d", rec.Code)
	}

	buf.Reset()
	liveReq := httptest.NewRequest("GET", "/api/v1/trips/x/fixes", nil)
	writeError(httptest.NewRecorder(), liveReq, errors.New("boom: a real server fault"))
	out = buf.String()
	if !strings.Contains(out, "level=ERROR") {
		t.Fatalf("expected a genuine internal error on a live request to still log at ERROR, got: %s", out)
	}

	// apierr.Error values never go through the unhandled-internal-error
	// path at all, regardless of context state.
	buf.Reset()
	writeError(httptest.NewRecorder(), req, apierr.New(apierr.CodeNotFound, "not found"))
	if buf.Len() != 0 {
		t.Fatalf("expected no log output for a well-formed apierr.Error, got: %s", buf.String())
	}
}
