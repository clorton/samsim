package inspectwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// fakeConn is a test double for Conn: a simple in-memory queue of frames to
// deliver, with an optional error to return once the queue is drained (or
// immediately, if set and the queue is empty).
type fakeConn struct {
	mu     sync.Mutex
	frames [][]byte
	err    error // returned after frames are exhausted
	closed bool
}

func newFakeConn(frames ...any) *fakeConn {
	fc := &fakeConn{}
	for _, f := range frames {
		data, err := json.Marshal(f)
		if err != nil {
			panic(err)
		}
		fc.frames = append(fc.frames, data)
	}
	return fc
}

func (c *fakeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	if len(c.frames) > 0 {
		f := c.frames[0]
		c.frames = c.frames[1:]
		c.mu.Unlock()
		return f, nil
	}
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Block without holding the mutex, so a concurrent Close() (which only
	// needs the mutex briefly to set c.closed) is never stalled by a
	// still-pending read -- mirroring how a real WebSocket connection's
	// Close() can interrupt a blocked Read().
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// fakeDialer returns a scripted sequence of connections, one per Dial call.
// Extra Dial calls beyond the script reuse the last entry.
type fakeDialer struct {
	mu    sync.Mutex
	conns []*fakeConn
	calls int
}

func newFakeDialer(conns ...*fakeConn) *fakeDialer {
	return &fakeDialer{conns: conns}
}

func (d *fakeDialer) Dial(ctx context.Context, vehicleID string) (Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	idx := d.calls
	if idx >= len(d.conns) {
		idx = len(d.conns) - 1
	}
	d.calls++
	if idx < 0 {
		return nil, fmt.Errorf("no connections scripted")
	}
	return d.conns[idx], nil
}

func (d *fakeDialer) dialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}
