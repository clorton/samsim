package inspectwatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"gpsdemo/internal/inspectclient"
)

// newTestRestClient builds a real inspectclient.Client against an
// httptest.Server serving just GET /api/v1/vehicles/{id}/snapshot, which is
// all Session needs from the REST side.
func newTestRestClient(t *testing.T, handler http.HandlerFunc) (*inspectclient.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &inspectclient.Client{BaseURL: u, HTTPClient: srv.Client(), MaxResponseBytes: 1 << 20}
	return client, srv
}

func snapshotHandler(databaseID, vehicleID string, revision int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"vehicle": map[string]any{
				"vehicle_id": vehicleID, "latest_fix": nil, "active_trip_id": nil,
				"revision": revision, "database_id": databaseID, "trip_gap_seconds": 600, "expected_fix_interval_ms": 1000,
			},
			"trips": []any{}, "manual_barriers": []any{}, "revision": revision, "database_id": databaseID,
		})
	}
}

func notFoundHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "NOT_FOUND", "message": "no", "details": map[string]any{}}})
	}
}

func instantSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// TestI09_ReadyFirstThenSnapshot_CoveredEventsDiscarded verifies the
// subscribe-first handshake: the ready frame registers revision/database,
// a snapshot is then fetched, and any envelope whose revision is <= the
// installed snapshot's revision is silently discarded.
func TestI09_ReadyFirstThenSnapshot_CoveredEventsDiscarded(t *testing.T) {
	rest, srv := newTestRestClient(t, snapshotHandler("db1", "v1", 5))
	defer srv.Close()

	conn := newFakeConn(
		readyFrame{Type: "ready", DatabaseID: "db1", VehicleID: "v1", Revision: 5},
		envelopeFrame{DatabaseID: "db1", VehicleID: "v1", Revision: 4, Type: "vehicle.changed"}, // covered, must be ignored
		envelopeFrame{DatabaseID: "db1", VehicleID: "v1", Revision: 6, Type: "vehicle.changed"}, // next, must be delivered
	)
	dialer := newFakeDialer(conn)

	sess := NewSession(Options{Dialer: dialer, RestClient: rest, VehicleID: "v1", ReconnectAttempts: 0, Sleep: instantSleep})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var events []Event
	done := make(chan struct{})
	go func() {
		sess.Run(ctx, func(ev Event) {
			events = append(events, ev)
			if len(events) >= 2 {
				cancel()
			}
		})
		close(done)
	}()
	<-done

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (snapshot + one envelope), got %d: %+v", len(events), events)
	}
	if events[0].Kind != EventSnapshot {
		t.Fatalf("expected first event to be a snapshot, got %s", events[0].Kind)
	}
	if events[0].Revision != 5 {
		t.Fatalf("expected snapshot revision 5, got %d", events[0].Revision)
	}
	for _, ev := range events[1:] {
		if ev.Kind == EventEnvelope && ev.Revision == 4 {
			t.Fatalf("expected revision 4 (covered by snapshot) to be discarded, but it was delivered")
		}
	}
	foundNext := false
	for _, ev := range events {
		if ev.Kind == EventEnvelope && ev.Revision == 6 {
			foundNext = true
		}
	}
	if !foundNext {
		t.Fatalf("expected revision 6 to be delivered: %+v", events)
	}
}

// TestI09_RevisionGapTriggersResync verifies a non-consecutive revision
// jump invalidates the session and triggers a reconnect+resync.
func TestI09_RevisionGapTriggersResync(t *testing.T) {
	rest, srv := newTestRestClient(t, snapshotHandler("db1", "v1", 1))
	defer srv.Close()

	firstConn := newFakeConn(
		readyFrame{Type: "ready", DatabaseID: "db1", VehicleID: "v1", Revision: 1},
		envelopeFrame{DatabaseID: "db1", VehicleID: "v1", Revision: 3, Type: "vehicle.changed"}, // gap: expected 2
	)
	secondConn := newFakeConn(
		readyFrame{Type: "ready", DatabaseID: "db1", VehicleID: "v1", Revision: 1},
	)
	dialer := newFakeDialer(firstConn, secondConn)

	sess := NewSession(Options{Dialer: dialer, RestClient: rest, VehicleID: "v1", ReconnectAttempts: 2, Sleep: instantSleep})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var sawResync, sawDisconnected bool
	var count int
	done := make(chan struct{})
	go func() {
		sess.Run(ctx, func(ev Event) {
			count++
			if ev.Kind == EventResync {
				sawResync = true
			}
			if ev.Kind == EventDisconnected {
				sawDisconnected = true
			}
			if sawResync {
				cancel()
			}
			if count > 20 {
				cancel()
			}
		})
		close(done)
	}()
	<-done

	if !sawDisconnected {
		t.Fatal("expected a disconnected event after the revision gap")
	}
	if !sawResync {
		t.Fatal("expected a resync event after reconnecting")
	}
	if dialer.dialCount() < 2 {
		t.Fatalf("expected at least 2 dial attempts, got %d", dialer.dialCount())
	}
}

// TestI09_DatabaseChangeTriggersResync verifies a database_id mismatch
// between the ready frame and the fetched snapshot invalidates the session.
func TestI09_DatabaseChangeTriggersResync(t *testing.T) {
	rest, srv := newTestRestClient(t, snapshotHandler("db-NEW", "v1", 1))
	defer srv.Close()

	conn := newFakeConn(
		readyFrame{Type: "ready", DatabaseID: "db-OLD", VehicleID: "v1", Revision: 1},
	)
	dialer := newFakeDialer(conn, newFakeConn(readyFrame{Type: "ready", DatabaseID: "db-NEW", VehicleID: "v1", Revision: 1}))

	sess := NewSession(Options{Dialer: dialer, RestClient: rest, VehicleID: "v1", ReconnectAttempts: 1, Sleep: instantSleep})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var sawDisconnected bool
	done := make(chan struct{})
	go func() {
		sess.Run(ctx, func(ev Event) {
			if ev.Kind == EventDisconnected {
				sawDisconnected = true
				cancel()
			}
		})
		close(done)
	}()
	<-done
	if !sawDisconnected {
		t.Fatal("expected a disconnect/resync triggered by the database_id mismatch")
	}
}

// TestI10_UnknownVehicleWaiting verifies a 404 snapshot (vehicle not yet
// created) emits a waiting record and installs the snapshot once an event
// proves the vehicle now exists.
func TestI10_UnknownVehicleWaiting(t *testing.T) {
	var callCount int
	rest, srv := newTestRestClient(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			notFoundHandler()(w, r)
			return
		}
		snapshotHandler("db1", "v1", 1)(w, r)
	})
	defer srv.Close()

	conn := newFakeConn(
		readyFrame{Type: "ready", DatabaseID: "db1", VehicleID: "v1", Revision: 0},
		envelopeFrame{DatabaseID: "db1", VehicleID: "v1", Revision: 1, Type: "vehicle.changed"},
	)
	dialer := newFakeDialer(conn)

	sess := NewSession(Options{Dialer: dialer, RestClient: rest, VehicleID: "v1", ReconnectAttempts: 0, Sleep: instantSleep})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var sawWaiting, sawSnapshot bool
	done := make(chan struct{})
	go func() {
		sess.Run(ctx, func(ev Event) {
			if ev.Kind == EventWaiting {
				sawWaiting = true
			}
			if ev.Kind == EventSnapshot {
				sawSnapshot = true
				cancel()
			}
		})
		close(done)
	}()
	<-done
	if !sawWaiting {
		t.Fatal("expected a waiting record for the unknown vehicle")
	}
	if !sawSnapshot {
		t.Fatal("expected a snapshot to eventually install once the vehicle exists")
	}
}

// TestI10_ReconnectExhaustion verifies that after ReconnectAttempts
// failures, Run returns an error and emits a terminal error event.
func TestI10_ReconnectExhaustion(t *testing.T) {
	rest, srv := newTestRestClient(t, snapshotHandler("db1", "v1", 1))
	defer srv.Close()

	// Every dial fails outright.
	dialer := &alwaysFailDialer{}

	sess := NewSession(Options{Dialer: dialer, RestClient: rest, VehicleID: "v1", ReconnectAttempts: 2, Sleep: instantSleep})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var sawError bool
	err := sess.Run(ctx, func(ev Event) {
		if ev.Kind == EventError {
			sawError = true
		}
	})
	if err == nil {
		t.Fatal("expected Run to return an error after exhausting reconnect attempts")
	}
	if !sawError {
		t.Fatal("expected a terminal error event")
	}
}

type alwaysFailDialer struct{}

func (d *alwaysFailDialer) Dial(ctx context.Context, vehicleID string) (Conn, error) {
	return nil, errAlwaysFail
}

var errAlwaysFail = &dialFailError{}

type dialFailError struct{}

func (e *dialFailError) Error() string { return "dial always fails" }

// TestI10_SIGINT_ExitsPromptly verifies that canceling ctx (simulating
// SIGINT) causes Run to return promptly with ctx.Err(), even mid-wait.
func TestI10_SIGINT_ExitsPromptly(t *testing.T) {
	rest, srv := newTestRestClient(t, snapshotHandler("db1", "v1", 1))
	defer srv.Close()

	conn := newFakeConn(readyFrame{Type: "ready", DatabaseID: "db1", VehicleID: "v1", Revision: 1})
	dialer := newFakeDialer(conn)

	sess := NewSession(Options{Dialer: dialer, RestClient: rest, VehicleID: "v1", ReconnectAttempts: 10, Sleep: instantSleep})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sess.Run(ctx, func(ev Event) {})
	}()

	time.Sleep(50 * time.Millisecond) // let the snapshot install
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit promptly after cancellation")
	}
}
