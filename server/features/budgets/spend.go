package budgets

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/counter"
)

// Scopes a budget can cap.
const (
	ScopeProject   = "project"
	ScopeKey       = "key"
	ScopeDeveloper = "developer"
	ScopeRoute     = "route"
)

// Spend counts what each scope of a project spends, per day and month, in
// micro-USD: the project, each API key, each developer and each route,
// whether or not a budget caps them yet, so a budget added mid-month starts
// from what was already spent.
type Spend struct {
	c *counter.Counters
}

// NewSpend counts spend on c (named "spend").
func NewSpend(c *counter.Counters) *Spend { return &Spend{c: c} }

// Who is what a call's spend counts against.
type Who struct {
	Project   uuid.UUID
	Key       uuid.UUID
	Developer string
	Route     string
}

// Key is the counter of one scope's spend; it names the project, so purges
// find it.
func Key(project uuid.UUID, scope, target string) string {
	return project.String() + ":" + scope + ":" + target
}

// targets lists the scopes a call counts against.
func (w Who) targets() [][2]string {
	out := [][2]string{{ScopeProject, ""}}
	if w.Key != uuid.Nil {
		out = append(out, [2]string{ScopeKey, w.Key.String()})
	}
	if w.Developer != "" {
		out = append(out, [2]string{ScopeDeveloper, w.Developer})
	}
	if w.Route != "" {
		out = append(out, [2]string{ScopeRoute, w.Route})
	}
	return out
}

// Add counts a call's cost against every scope it belongs to.
func (s *Spend) Add(w Who, micros int64) {
	if micros <= 0 {
		return
	}
	for _, t := range w.targets() {
		k := Key(w.Project, t[0], t[1])
		s.c.Add(k, counter.Day, micros)
		s.c.Add(k, counter.Month, micros)
	}
}

// Spent is a scope's spend in the current window, as the gateway sees it
// (its own count plus everyone's at the last flush).
func (s *Spend) Spent(ctx context.Context, project uuid.UUID, scope, target string, w counter.Window) (int64, error) {
	return s.c.Value(ctx, Key(project, scope, target), w)
}

// Total is a scope's spend in the window holding at, read from Valkey.
func (s *Spend) Total(ctx context.Context, project uuid.UUID, scope, target string, w counter.Window, at time.Time) (int64, error) {
	return s.c.Total(ctx, Key(project, scope, target), w, at)
}

func windowOf(name string) counter.Window {
	if name == "day" {
		return counter.Day
	}
	return counter.Month
}
