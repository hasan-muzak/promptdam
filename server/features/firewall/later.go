package firewall

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Later reads, after a call went, what the detector model finds for labels
// that only alert: no call waits on it. A few workers on a bounded queue;
// when the queue is full the read is dropped and counted, never waited for.
type Later struct {
	jobs    chan func()
	wg      sync.WaitGroup
	dropped atomic.Int64
}

// Later's size: model reads are tens of milliseconds, the service batches.
const (
	laterWorkers = 4
	laterQueue   = 256
)

// NewLater returns a Later; Start runs its workers.
func NewLater() *Later { return &Later{jobs: make(chan func(), laterQueue)} }

// Start runs the workers.
func (l *Later) Start(context.Context) error {
	for range laterWorkers {
		l.wg.Go(func() {
			for job := range l.jobs {
				job()
			}
		})
	}
	return nil
}

// Stop finishes the queued reads.
func (l *Later) Stop(ctx context.Context) error {
	close(l.jobs)
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Offer queues a read; false when the queue is full.
func (l *Later) Offer(job func()) bool {
	select {
	case l.jobs <- job:
		return true
	default:
		if n := l.dropped.Add(1); n == 1 || n%1000 == 0 {
			slog.Warn("firewall: a model read for alerts was dropped, the queue is full", "dropped", n)
		}
		return false
	}
}

// Dropped is how many reads were dropped.
func (l *Later) Dropped() int64 { return l.dropped.Load() }

// ModelReads reports whether the model reads this project's calls (some
// label it finds acts, alerts included) and whether a call waits for it
// (some label masks or blocks: what leaves must be decided before).
func (e *Engine) ModelReads() (reads, waits bool) {
	if e == nil {
		return false, false
	}
	for _, l := range ModelLabels {
		switch e.s.labelAction(l) {
		case Off:
		case Alert:
			reads = true
		default:
			return true, true
		}
	}
	return reads, false
}

// ModelHits are the model's hits of o: what a read after the call went
// adds to its events.
func ModelHits(o *Outcome) []Hit {
	var out []Hit
	for _, h := range o.Hits {
		if h.Model {
			out = append(out, h)
		}
	}
	return out
}
