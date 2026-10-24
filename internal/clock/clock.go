// Package clock provides an injectable wall clock so tests can control
// server commit-associated timestamps deterministically (AG-01, AG-10).
package clock

import (
	"sync"
	"time"
)

// Clock returns the current wall-clock time. Implementations must be safe
// for concurrent use.
type Clock interface {
	Now() time.Time
	// NowMs returns the current time as Unix epoch milliseconds.
	NowMs() int64
}

// System is a Clock backed by the real system clock.
type System struct{}

func (System) Now() time.Time { return time.Now() }

func (System) NowMs() int64 { return time.Now().UnixMilli() }

// Fixed is a Clock that always returns the same instant, with optional
// manual advancement, for deterministic tests. Safe for concurrent use.
type Fixed struct {
	mu sync.Mutex
	t  time.Time
}

// NewFixed returns a Fixed clock starting at t.
func NewFixed(t time.Time) *Fixed {
	return &Fixed{t: t}
}

func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *Fixed) NowMs() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t.UnixMilli()
}

// Advance moves the fixed clock forward by d.
func (f *Fixed) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// Set assigns the fixed clock to t.
func (f *Fixed) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = t
}
