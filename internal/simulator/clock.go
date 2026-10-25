package simulator

import (
	"math"
	"sync"
	"time"
)

// MonotonicClock provides monotonic time readings for virtual-clock
// calculations. Only differences between two readings are meaningful;
// absolute values are not interpreted. Implementations must be safe for
// concurrent use.
type MonotonicClock interface {
	Now() time.Time
}

// SystemMonotonicClock is a MonotonicClock backed by time.Now(), which on
// all supported platforms includes a monotonic reading alongside the wall
// clock reading as long as the Time value is not serialized/deserialized or
// has had its monotonic reading stripped by methods like Round or
// Truncate.
type SystemMonotonicClock struct{}

// Now returns the current time, as reported by time.Now().
func (SystemMonotonicClock) Now() time.Time { return time.Now() }

// FakeMonotonicClock is a MonotonicClock for tests: it starts at an
// arbitrary fixed instant and only advances when Advance is called
// explicitly. Safe for concurrent use.
type FakeMonotonicClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeMonotonicClock returns a FakeMonotonicClock initialized to start.
func NewFakeMonotonicClock(start time.Time) *FakeMonotonicClock {
	return &FakeMonotonicClock{now: start}
}

// Now returns the clock's current (fake) instant.
func (c *FakeMonotonicClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock's current instant forward by d. d should
// normally be non-negative; this is not enforced, but a negative d would
// move the fake clock backward, which real monotonic clocks never do.
func (c *FakeMonotonicClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// VirtualClock tracks simulated playback time ("virtual elapsed
// milliseconds since run start") separately from wall-clock/monotonic
// time, advancing at a configurable multiplier while running and freezing
// while stopped/paused, per SIM-02.
//
// Design note on drift: rather than incrementally accumulating small
// per-tick deltas (which would accumulate floating-point rounding error
// over many timer wakeups), VirtualElapsedMs always recomputes from a
// fixed "anchor" (the virtual-elapsed value and monotonic instant at the
// last state transition) using the full-precision monotonic duration since
// that anchor. The anchor only moves at explicit transition points (Start,
// Stop, SetMultiplier), never incrementally — so no drift can accumulate
// across repeated queries of a single running segment.
type VirtualClock struct {
	mu sync.Mutex

	mono MonotonicClock

	running    bool
	multiplier float64

	// anchorVirtualMs is the virtual elapsed time, in milliseconds, at the
	// instant the current anchor was established.
	anchorVirtualMs int64
	// anchorMonoTime is the monotonic instant at which anchorVirtualMs was
	// established. Only meaningful while running.
	anchorMonoTime time.Time
}

// NewVirtualClock returns a VirtualClock in the stopped state, with virtual
// elapsed time 0 and the given initial multiplier.
func NewVirtualClock(mono MonotonicClock, initialMultiplier float64) *VirtualClock {
	return &VirtualClock{
		mono:       mono,
		multiplier: initialMultiplier,
	}
}

// Start transitions the clock to running, anchoring virtual-elapsed
// advancement from the current virtual elapsed value (0 for a fresh clock,
// or the frozen value from the last Stop for a resumed one) at the current
// monotonic instant. Calling Start while already running is a no-op (it
// does not reset the anchor or otherwise disturb virtual time).
func (c *VirtualClock) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return
	}
	c.running = true
	c.anchorMonoTime = c.mono.Now()
	// anchorVirtualMs already holds the frozen value from the last Stop
	// (or 0 for a fresh clock).
}

// Stop freezes the clock: it computes the current virtual elapsed value
// (using the formula above) and anchors future Start calls to resume from
// exactly that value, without counting any wall/monotonic time that passes
// while stopped. Calling Stop while already stopped is a no-op.
func (c *VirtualClock) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return
	}
	c.anchorVirtualMs = c.virtualElapsedMsLocked()
	c.running = false
}

// Running reports whether the clock is currently running.
func (c *VirtualClock) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// VirtualElapsedMs returns the current virtual elapsed time in
// milliseconds, computed from the current anchor if running, or the frozen
// value if stopped.
func (c *VirtualClock) VirtualElapsedMs() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.virtualElapsedMsLocked()
}

// virtualElapsedMsLocked computes the current virtual elapsed time. Callers
// must hold c.mu.
func (c *VirtualClock) virtualElapsedMsLocked() int64 {
	if !c.running {
		return c.anchorVirtualMs
	}
	elapsed := c.mono.Now().Sub(c.anchorMonoTime)
	deltaMs := int64(math.Round(elapsed.Seconds() * 1000 * c.multiplier))
	return c.anchorVirtualMs + deltaMs
}

// SetMultiplier changes the clock multiplier. Per spec: "A multiplier
// change first advances virtual time using the old multiplier, then
// installs new anchors" — meaning if the clock is currently running, this
// method must (1) compute and freeze the virtual-elapsed value accumulated
// so far under the OLD multiplier up to this exact instant, (2) install a
// fresh anchor at the current monotonic instant with that frozen value,
// and (3) only then switch to the new multiplier for future advancement.
// This must produce continuous (no jump, no backward movement) virtual
// time across the change. If the clock is stopped, this simply updates the
// multiplier for when it next starts running.
func (c *VirtualClock) SetMultiplier(newMultiplier float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		now := c.mono.Now()
		elapsed := now.Sub(c.anchorMonoTime)
		deltaMs := int64(math.Round(elapsed.Seconds() * 1000 * c.multiplier))
		c.anchorVirtualMs += deltaMs
		c.anchorMonoTime = now
	}
	c.multiplier = newMultiplier
}

// Multiplier returns the current multiplier.
func (c *VirtualClock) Multiplier() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.multiplier
}

// ChunkDueIndices returns up to maxChunkSize consecutive indices into a
// hypothetical sorted array, starting at startIndex (inclusive) and ending
// before endIndexExclusive, as a sequence of [start, end) pairs each no
// larger than maxChunkSize. This lets a caller processing a large
// catch-up batch (e.g. after a long pause or slow timer wakeup) break the
// work into bounded chunks and yield control (e.g. to service a pending
// control-plane request) between chunks, without re-deriving or
// duplicating which indices have already been processed.
//
// Example: ChunkDueIndices(0, 2500, 1000) yields (0,1000), (1000,2000),
// (2000,2500).
//
// If endIndexExclusive <= startIndex, or maxChunkSize <= 0, no chunks are
// returned.
func ChunkDueIndices(startIndex, endIndexExclusive, maxChunkSize int) [][2]int {
	if endIndexExclusive <= startIndex || maxChunkSize <= 0 {
		return nil
	}
	var chunks [][2]int
	for start := startIndex; start < endIndexExclusive; start += maxChunkSize {
		end := start + maxChunkSize
		if end > endIndexExclusive {
			end = endIndexExclusive
		}
		chunks = append(chunks, [2]int{start, end})
	}
	return chunks
}
