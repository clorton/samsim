package simulator

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestIsClientGone mirrors internal/httpapi's equivalent test: a canceled
// request context (routine when a live-polling dashboard aborts an
// in-flight fetch) must be classified as client-gone regardless of the
// specific wrapped error, while a live context with an unrelated error must
// not be.
func TestIsClientGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"direct context.Canceled", context.Canceled, true},
		{"wrapped context.Canceled", fmt.Errorf("wrapped: %w", context.Canceled), true},
		{"unrelated error on canceled ctx", errors.New("boom"), true}, // ctx.Err() alone is sufficient
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isClientGone(ctx, c.err); got != c.want {
				t.Fatalf("isClientGone(canceled ctx, %v) = %v, want %v", c.err, got, c.want)
			}
		})
	}

	if isClientGone(context.Background(), errors.New("boom")) {
		t.Fatal("expected a live context with an unrelated error to not be classified as client-gone")
	}
}

// TestIsWriteBrokenByClient recognizes the concrete error strings observed
// in practice without misclassifying an unrelated encoding error.
func TestIsWriteBrokenByClient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"broken pipe", errors.New("write tcp 127.0.0.1:8090->127.0.0.1:61564: write: broken pipe"), true},
		{"connection reset", errors.New("write tcp 127.0.0.1:8090->127.0.0.1:61564: write: connection reset by peer"), true},
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
