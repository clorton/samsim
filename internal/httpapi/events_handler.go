package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"gpsdemo/internal/apierr"
	"gpsdemo/internal/events"
)

// writeDeadline bounds each individual WebSocket write (AG-08: "per-client
// bounded queues, deadlines, ping/pong and cancellation").
const writeDeadline = 5 * time.Second

// pingInterval governs keepalive pings to detect dead connections.
const pingInterval = 30 * time.Second

// readyFrameDTO and envelopeDTO mirror AG-08's JSON wire shapes.
type readyFrameDTO struct {
	Type       string `json:"type"`
	DatabaseID string `json:"database_id"`
	VehicleID  string `json:"vehicle_id"`
	Revision   int64  `json:"revision"`
}

type changeDTO struct {
	Type               string   `json:"type"`
	TripID             string   `json:"trip_id,omitempty"`
	CloseReason        string   `json:"close_reason,omitempty"`
	PredecessorTripIDs []string `json:"predecessor_trip_ids,omitempty"`
	SuccessorTripID    string   `json:"successor_trip_id,omitempty"`
	ActiveTripID       *string  `json:"active_trip_id,omitempty"`
	LatestTimestampMs  *int64   `json:"latest_timestamp_ms,omitempty"`
	QuarantinedCount   int      `json:"quarantined_count,omitempty"`
}

type envelopeDTO struct {
	EventID     string      `json:"event_id"`
	DatabaseID  string      `json:"database_id"`
	VehicleID   string      `json:"vehicle_id"`
	Revision    int64       `json:"revision"`
	Type        string      `json:"type"`
	TimestampMs int64       `json:"timestamp_ms"`
	Changes     []changeDTO `json:"changes"`
}

func toEnvelopeDTO(e events.Envelope) envelopeDTO {
	dto := envelopeDTO{
		EventID:     e.EventID,
		DatabaseID:  e.DatabaseID,
		VehicleID:   e.VehicleID,
		Revision:    e.Revision,
		Type:        e.Type,
		TimestampMs: e.TimestampMs,
	}
	for _, c := range e.Changes {
		dto.Changes = append(dto.Changes, changeDTO{
			Type:               string(c.Type),
			TripID:             c.TripID,
			CloseReason:        c.CloseReason,
			PredecessorTripIDs: c.PredecessorTripIDs,
			SuccessorTripID:    c.SuccessorTripID,
			ActiveTripID:       c.ActiveTripID,
			LatestTimestampMs:  c.LatestTimestampMs,
			QuarantinedCount:   c.QuarantinedCount,
		})
	}
	return dto
}

// handleEventsWebSocket implements WS /api/v1/events?vehicle_id=... (AG-08).
// Service.SubscribeReady determines the ready revision and registers the
// Hub subscription atomically under the storage writer coordinator lock, so
// no commit can land between the two.
func (s *Server) handleEventsWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}

	vehicleID := r.URL.Query().Get("vehicle_id")
	if vehicleID == "" {
		writeError(w, r, apierr.New(apierr.CodeInvalidRequest, "vehicle_id query parameter is required"))
		return
	}

	// Reject malformed filters before upgrade (AG-08).
	if err := validateVehicleIDFilter(vehicleID); err != nil {
		writeError(w, r, err)
		return
	}

	readyRevision, sub, err := s.Service.SubscribeReady(r.Context(), vehicleID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer s.Hub.Unsubscribe(sub)

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "websocket accept failed", "error", err)
		return
	}
	defer c.CloseNow()

	// This handler never reads application messages, only sends them. Call
	// CloseRead so the connection still responds to ping/pong/close control
	// frames (letting c.Ping and c.Close work, and client-initiated closes
	// complete promptly) and so the returned context is cancelled as soon as
	// the connection closes for any reason.
	ctx := c.CloseRead(r.Context())

	writeCtx, cancel := context.WithTimeout(ctx, writeDeadline)
	err = wsjson.Write(writeCtx, c, readyFrameDTO{
		Type: "ready", DatabaseID: s.Service.Store.DatabaseID, VehicleID: vehicleID, Revision: readyRevision,
	})
	cancel()
	if err != nil {
		return
	}

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "server shutting down")
			return
		case <-pingTicker.C:
			pingCtx, cancel := context.WithTimeout(ctx, writeDeadline)
			err := c.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		case env, ok := <-sub.Envelopes():
			if !ok {
				if sub.Overflowed() {
					c.Close(websocket.StatusTryAgainLater, "event queue overflow; reconnect and resnapshot")
				}
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, writeDeadline)
			err := wsjson.Write(writeCtx, c, toEnvelopeDTO(env))
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func validateVehicleIDFilter(vehicleID string) error {
	if len(vehicleID) == 0 || len(vehicleID) > 128 {
		return apierr.New(apierr.CodeInvalidRequest, "vehicle_id must be 1-128 bytes")
	}
	for _, r := range vehicleID {
		if !isValidIDRune(r) {
			return apierr.New(apierr.CodeInvalidRequest, "vehicle_id contains invalid characters")
		}
	}
	return nil
}

func isValidIDRune(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
}
