package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// TestEventsWebSocket_ContiguousRevisions implements part of T16: multiple
// changed-transaction envelopes arrive with strictly contiguous revisions.
func TestEventsWebSocket_ContiguousRevisions(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	c := dialEvents(t, ts.URL, "vehicle-001")
	defer c.Close(websocket.StatusNormalClosure, "")

	readFrame := func(v any) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := wsjson.Read(ctx, c, v); err != nil {
			t.Fatal(err)
		}
	}

	var ready readyFrameDTO
	readFrame(&ready)
	if ready.Revision != 0 {
		t.Fatalf("expected ready revision 0, got %d", ready.Revision)
	}

	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0)
	var env1 envelopeDTO
	readFrame(&env1)
	if env1.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", env1.Revision)
	}

	postPacket(t, "sim-1", ts.URL, "20261024-000002", 1000)
	var env2 envelopeDTO
	readFrame(&env2)
	if env2.Revision != 2 {
		t.Fatalf("expected revision 2 (contiguous), got %d", env2.Revision)
	}
}

// TestEventsWebSocket_NoEventOnDuplicatePacket implements part of T16: a
// duplicate packet replay must not produce a new event.
func TestEventsWebSocket_NoEventOnDuplicatePacket(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	c := dialEvents(t, ts.URL, "vehicle-001")
	defer c.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ready readyFrameDTO
	if err := wsjson.Read(ctx, c, &ready); err != nil {
		t.Fatal(err)
	}

	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	var env envelopeDTO
	if err := wsjson.Read(ctx2, c, &env); err != nil {
		t.Fatal(err)
	}
	if env.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", env.Revision)
	}

	// Replay the exact same packet.
	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0)

	// No further event should arrive; read with a short timeout and expect
	// it to expire.
	ctx3, cancel3 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel3()
	var env2 envelopeDTO
	err := wsjson.Read(ctx3, c, &env2)
	if err == nil {
		t.Fatalf("expected no event on duplicate packet replay, got %+v", env2)
	}
}

// TestEventsWebSocket_ReconnectResnapshot implements part of T16: a fresh
// subscription after some state exists returns the current revision in its
// ready frame (simulating the reconnect/resnapshot client flow).
func TestEventsWebSocket_ReconnectResnapshot(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)

	postPacket(t, "sim-1", ts.URL, "20261024-000001", 0)
	postPacket(t, "sim-1", ts.URL, "20261024-000002", 1000)

	c := dialEvents(t, ts.URL, "vehicle-001")
	defer c.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ready readyFrameDTO
	if err := wsjson.Read(ctx, c, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Revision != 2 {
		t.Fatalf("expected fresh subscription to report current revision 2, got %d", ready.Revision)
	}

	// The snapshot fetched after ready must agree.
	_, snap := getJSON(t, ts.URL+"/api/v1/vehicles/vehicle-001/snapshot")
	if int64(snap["revision"].(float64)) != ready.Revision {
		t.Fatalf("expected snapshot revision to match ready revision, got %v vs %d", snap["revision"], ready.Revision)
	}
}
