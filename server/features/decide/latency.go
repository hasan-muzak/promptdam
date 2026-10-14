package decide

import (
	"slices"
	"sync"
	"time"
)

// Auto timeouts: how long a decision model is waited for when its router
// sets no timeout, from how long its latest answers took on this gateway.
// Waiting longer delays the call; giving up serves the default tier, which
// costs the saving. The wait covers the model's slow answers but not its
// stalls.
const (
	// autoFirst is the wait until autoKnown answers are measured.
	autoFirst = 2 * time.Second
	// autoMin and autoMax bound a measured wait.
	autoMin = 200 * time.Millisecond
	autoMax = 3 * time.Second
	// autoSamples is how many of a model's latest answers are kept, and
	// autoKnown how many make its wait measured.
	autoSamples = 128
	autoKnown   = 20
	// maxTracked bounds the decision models a gateway tracks.
	maxTracked = 4096
)

// latencies keeps how long each decision target's latest answers took.
type latencies struct {
	mu   sync.Mutex
	byID map[string]*ring
}

// ring is a target's latest answers' latencies, oldest overwritten.
type ring struct {
	took    [autoSamples]time.Duration
	n, next int
}

// add records an answer of target id that took took.
func (l *latencies) add(id string, took time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.byID[id]
	if r == nil {
		if len(l.byID) >= maxTracked {
			l.byID = map[string]*ring{} // more models than any gateway asks: start over
		}
		r = &ring{}
		l.byID[id] = r
	}
	r.took[r.next] = took
	r.next = (r.next + 1) % autoSamples
	r.n = min(r.n+1, autoSamples)
}

// timeout is how long to wait for target id: a quarter more than its
// latest answers' 99th percentile (a question that timed out counting as
// taking its whole wait, so a wait too short grows), within autoMin and
// autoMax; autoFirst until autoKnown answers are measured. Sorting at most
// autoSamples durations is constant work.
func (l *latencies) timeout(id string) time.Duration {
	var took [autoSamples]time.Duration
	l.mu.Lock()
	r := l.byID[id]
	n := 0
	if r != nil {
		n = copy(took[:], r.took[:r.n])
	}
	l.mu.Unlock()
	if n < autoKnown {
		return autoFirst
	}
	sorted := took[:n]
	slices.Sort(sorted)
	p99 := sorted[(n*99+99)/100-1] // the ceil(0.99n)-th
	return min(max(p99*5/4, autoMin), autoMax)
}
