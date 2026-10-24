package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func dialEvents(t *testing.T, ts string, vehicleID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts, "http") + "/api/v1/events?vehicle_id=" + vehicleID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func TestEventsWebSocket_ReadyFrameFirst(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	c := dialEvents(t, ts.URL, "vehicle-001")
	defer c.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ready readyFrameDTO
	if err := wsjson.Read(ctx, c, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Type != "ready" {
		t.Fatalf("expected ready frame, got %+v", ready)
	}
	if ready.VehicleID != "vehicle-001" {
		t.Fatalf("unexpected vehicle_id: %+v", ready)
	}
	if ready.Revision != 0 {
		t.Fatalf("expected revision 0 for not-yet-created vehicle, got %d", ready.Revision)
	}
}

func TestEventsWebSocket_DeliversEnvelopeAfterIngest(t *testing.T) {
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
	if env.VehicleID != "vehicle-001" {
		t.Fatalf("unexpected vehicle_id: %+v", env)
	}
	if len(env.Changes) == 0 {
		t.Fatal("expected nonempty changes")
	}
}

func TestEventsWebSocket_RequiresVehicleIDFilter(t *testing.T) {
	_, ts := newTestServerAndHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/events"
	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("expected dial to fail without vehicle_id")
	}
	if resp != nil && resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
