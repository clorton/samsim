package inspectwatch

import (
	"context"
	"fmt"
	"net/url"

	"github.com/coder/websocket"
)

// wsDialer is the real Dialer, wrapping github.com/coder/websocket.
type wsDialer struct {
	baseURL          *url.URL
	maxResponseBytes int64
}

// NewDialer returns a Dialer that connects to baseURL's events endpoint,
// mapping http->ws and https->wss (INS-04: "Use WS for http and WSS for
// https, same host and escaped vehicle query").
func NewDialer(baseURL *url.URL, maxResponseBytes int64) Dialer {
	return &wsDialer{baseURL: baseURL, maxResponseBytes: maxResponseBytes}
}

func (d *wsDialer) Dial(ctx context.Context, vehicleID string) (Conn, error) {
	u := *d.baseURL
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return nil, fmt.Errorf("unsupported base URL scheme %q", u.Scheme)
	}
	u.Path = "/api/v1/events"
	q := url.Values{}
	q.Set("vehicle_id", vehicleID)
	u.RawQuery = q.Encode()

	c, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(d.maxResponseBytes)
	return &wsConn{c: c}, nil
}

type wsConn struct {
	c *websocket.Conn
}

func (w *wsConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, data, err := w.c.Read(ctx)
	return data, err
}

func (w *wsConn) Close() error {
	return w.c.Close(websocket.StatusNormalClosure, "watch ended")
}
