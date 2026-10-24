package httpapi

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gpsdemo/internal/aggregator"
	"gpsdemo/internal/clock"
	"gpsdemo/internal/events"
	"gpsdemo/internal/idgen"
	"gpsdemo/internal/storage"
)

func newTestServer(t *testing.T) (*Server, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(context.Background(), storage.Options{
		Path:                  filepath.Join(dir, "test.sqlite"),
		BusyTimeoutMs:         5000,
		TripGapSeconds:        600,
		ExpectedFixIntervalMs: 1000,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	hub := events.NewHub(16)
	clk := clock.NewFixed(time.Unix(1700000000, 0).UTC())
	ids := idgen.NewSequential("trip-")
	svc := aggregator.NewService(store, hub, clk, ids, 10000)

	s := &Server{
		Service:           svc,
		Hub:               hub,
		MaxBodyBytes:      1 << 20,
		MaxFixesPerPacket: 10000,
		SnapshotMaxBytes:  16 << 20,
	}
	return s, store
}

func newTestServerAndHTTP(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, _ := newTestServer(t)
	ts := httptestServer(s)
	t.Cleanup(ts.Close)
	return s, ts
}

func httptestServer(s *Server) *httptest.Server {
	return httptest.NewServer(s.Router())
}

const baseMs = int64(1792845600000)
