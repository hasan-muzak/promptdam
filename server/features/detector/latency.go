package detector

import (
	"slices"
	"sync"
	"time"
)

// Timeouts: how long a call waits for the service, from how long its
// latest answers took per KiB read, so a long text waits longer than a
// short one. Waiting longer delays the call; giving up falls back to the
// patterns (or refuses the call, as the project says).
const (
	firstWait = 2 * time.Second // until knownSamples answers are measured
	minWait   = 200 * time.Millisecond
	maxWait   = 3 * time.Second
	samples   = 128
	known     = 20
	baseWait  = 50 * time.Millisecond
)

// latency keeps the latest answers' time per KiB, oldest overwritten.
type latency struct {
	mu      sync.Mutex
	perKiB  [samples]time.Duration
	n, next int
}

// add records an answer for size bytes that took took (a timeout counts
// as its whole wait, so a wait too short grows).
func (l *latency) add(size int, took time.Duration) {
	kib := max(1, (size+1023)/1024)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.perKiB[l.next] = took / time.Duration(kib)
	l.next = (l.next + 1) % samples
	l.n = min(l.n+1, samples)
}

// timeout is how long to wait for size bytes: a quarter more than the
// latest answers' 99th percentile per KiB, times the KiB asked, within
// minWait and maxWait. Sorting at most samples durations is constant work.
func (l *latency) timeout(size int) time.Duration {
	var took [samples]time.Duration
	l.mu.Lock()
	n := copy(took[:], l.perKiB[:l.n])
	l.mu.Unlock()
	if n < known {
		return firstWait
	}
	sorted := took[:n]
	slices.Sort(sorted)
	p99 := sorted[(n*99+99)/100-1]
	kib := time.Duration(max(1, (size+1023)/1024))
	return min(max(baseWait+p99*kib*5/4, minWait), maxWait)
}
