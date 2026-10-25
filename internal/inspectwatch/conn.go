// Package inspectwatch implements gpsinspect's `watch` command (INS-04):
// the AG-08 subscribe-first ready/snapshot handshake, revision-gap
// detection, and reconnect-with-backoff state machine, independent of the
// human/NDJSON rendering layer (inspectoutput) and of any one WebSocket
// library's concrete connection type, so tests can supply a fake transport.
package inspectwatch

import "context"

// Conn abstracts a WebSocket connection to the events endpoint down to
// exactly the operations watch needs. The real implementation wraps
// github.com/coder/websocket (whose Read already handles ping/pong
// transparently and enforces a configured per-message size limit); tests
// implement a fake.
type Conn interface {
	// ReadMessage returns the next text frame's raw bytes, or an error if
	// the connection is closed, the context is done, or the frame exceeds
	// the configured size limit.
	ReadMessage(ctx context.Context) ([]byte, error)
	// Close closes the connection.
	Close() error
}

// Dialer opens a new Conn to the events endpoint for vehicleID. The real
// implementation builds a ws(s):// URL from the REST base URL (same host,
// scheme mapped http->ws / https->wss) and escapes the vehicle_id query
// value, and configures the connection's read size limit from
// max-response-bytes.
type Dialer interface {
	Dial(ctx context.Context, vehicleID string) (Conn, error)
}
