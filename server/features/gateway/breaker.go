package gateway

import (
	"sync"
	"time"
)

// Circuit breakers stop sending calls to a provider's model that keeps
// failing: after breakerFailures failures in a row its circuit opens for
// breakerOpen, then one call probes it; success closes it, failure opens it
// again. Each gateway process keeps its own.
const (
	breakerFailures = 5
	breakerOpen     = 30 * time.Second
	// maxBreakers bounds the circuits held: past it, all are forgotten
	// (closed), which only costs some calls to failing providers.
	maxBreakers = 50_000
)

type breakers struct {
	mu sync.Mutex
	m  map[string]*breaker
}

type breaker struct {
	failures  int
	openUntil time.Time
	probing   bool
}

func newBreakers() *breakers { return &breakers{m: map[string]*breaker{}} }

// allow reports whether a call may go to target id now.
func (bs *breakers) allow(id string, now time.Time) bool {
	if id == "" {
		return true
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	b := bs.m[id]
	switch {
	case b == nil || b.openUntil.IsZero():
		return true
	case now.Before(b.openUntil), b.probing:
		return false
	default:
		b.probing = true // the circuit half-opens: one call probes it
		return true
	}
}

// record notes a call's outcome.
func (bs *breakers) record(id string, ok bool, now time.Time) {
	if id == "" {
		return
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	b := bs.m[id]
	if ok {
		if b != nil {
			delete(bs.m, id)
		}
		return
	}
	if b == nil {
		if len(bs.m) >= maxBreakers {
			clear(bs.m)
		}
		b = &breaker{}
		bs.m[id] = b
	}
	b.failures++
	if b.probing || b.failures >= breakerFailures {
		b.openUntil, b.probing, b.failures = now.Add(breakerOpen), false, 0
	}
}
