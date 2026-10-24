// Package idgen provides an injectable opaque unique ID generator, used for
// trip IDs so tests can substitute deterministic sequences (AG-01).
package idgen

import (
	"fmt"
	"sync/atomic"

	"github.com/google/uuid"
)

// Generator produces opaque unique string IDs. Implementations must be safe
// for concurrent use.
type Generator interface {
	NewID() string
}

// UUID is a Generator backed by random UUIDs.
type UUID struct{}

func (UUID) NewID() string {
	return uuid.NewString()
}

// Sequential is a deterministic Generator for tests, producing IDs of the
// form "<prefix><n>" with a monotonically increasing counter starting at 1.
type Sequential struct {
	prefix  string
	counter atomic.Int64
}

// NewSequential returns a Sequential generator with the given ID prefix.
func NewSequential(prefix string) *Sequential {
	return &Sequential{prefix: prefix}
}

func (s *Sequential) NewID() string {
	n := s.counter.Add(1)
	return fmt.Sprintf("%s%d", s.prefix, n)
}
