package firewall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/events"
)

// EventsTable holds the firewall's events: per call, one per detector and
// place it found something in, or one for the policy that decided.
const EventsTable = "firewall_events"

// Event is one row of the firewall's events. Never a value: Excerpt, kept
// only when the project asks, has every finding masked.
type Event struct {
	ProjectID      uuid.UUID `ch:"project_id"`
	TS             time.Time `ch:"ts"`
	Trace          string    `ch:"trace"`
	OrgID          uuid.UUID `ch:"org_id"`
	KeyID          uuid.UUID `ch:"key_id"`
	Developer      string    `ch:"developer"`
	Route          string    `ch:"route"`
	RequestedModel string    `ch:"requested_model"`
	Provider       string    `ch:"provider"`
	// Detector, Class, Action and Location are the hit's (empty for a
	// policy's event but Action: block, allow or route_to).
	Detector string `ch:"detector"`
	Class    string `ch:"class"`
	Action   string `ch:"action"`
	Location string `ch:"location"`
	Count    uint32 `ch:"count"`
	// Blocked, BlockedBy and Policy are the call's.
	Blocked       bool   `ch:"blocked"`
	BlockedBy     string `ch:"blocked_by"`
	Policy        string `ch:"policy"`
	Excerpt       string `ch:"excerpt"`
	RetentionDays uint16 `ch:"retention_days"`
}

// EventColumns are the table's columns, in EventValues' order.
var EventColumns = []string{"project_id", "ts", "trace", "org_id", "key_id", "developer", "route", "requested_model", "provider",
	"detector", "class", "action", "location", "count", "blocked", "blocked_by", "policy", "excerpt", "retention_days"}

// EventValues are an event's values, in EventColumns' order.
func EventValues(e Event) []any {
	return []any{e.ProjectID, e.TS, e.Trace, e.OrgID, e.KeyID, e.Developer, e.Route, e.RequestedModel, e.Provider,
		e.Detector, e.Class, e.Action, e.Location, e.Count, e.Blocked, e.BlockedBy, e.Policy, e.Excerpt, e.RetentionDays}
}

// Events are what a call's outcome and its answer's hits leave in the
// table, the call's own fields (project, trace…) taken from call: none
// when the firewall found nothing and no policy decided.
func Events(call Event, o *Outcome, answer []Hit) []Event {
	if o == nil {
		return nil
	}
	call.Blocked, call.BlockedBy, call.Policy = o.Blocked, o.BlockedBy, o.Policy
	out := make([]Event, 0, len(o.Hits)+len(answer))
	for _, hits := range [][]Hit{o.Hits, answer} {
		for _, h := range hits {
			e := call
			e.Detector, e.Class, e.Action, e.Location, e.Count, e.Excerpt = h.Detector, h.Class, h.Action, h.Location,
				uint32(min(h.Count, 1<<31)), h.Excerpt
			out = append(out, e)
		}
	}
	if len(out) == 0 && (o.Blocked || o.Policy != "") {
		e := call
		switch {
		case o.Blocked:
			e.Action = Block
		case o.RouteTo != "":
			e.Action = PolicyRouteTo
		default:
			e.Action = PolicyAllow
		}
		out = append(out, e)
	}
	return out
}

// Query limits.
const (
	// MaxEventRange bounds a list of events, MaxStatsRange their stats.
	MaxEventRange = 31 * 24 * time.Hour
	MaxStatsRange = 90 * 24 * time.Hour
)

// Errors of the queries.
var (
	ErrRange  = errors.New("firewall: the time range must start before it ends and be at most 31 days (90 for stats)")
	ErrCursor = errors.New("firewall: the cursor is invalid")
)

// EventFilter selects events.
type EventFilter struct {
	From, To                          time.Time
	Detector, Class, Action, Location string
	Key                               uuid.UUID
	// Trace keeps one call's events.
	Trace string
	// Blocked keeps only blocked calls' events.
	Blocked bool
}

// EventCursor is where a page of events ends.
type EventCursor struct {
	TS              time.Time
	Trace, Detector string
	Location        string
}

// ListEvents returns up to limit events of a project, newest first, after
// the cursor; the next cursor is set when more follow.
func ListEvents(ctx context.Context, db *events.DB, project uuid.UUID, f EventFilter, after *EventCursor, limit int) (
	[]Event, *EventCursor, error) {
	if f.From.IsZero() || !f.From.Before(f.To) || f.To.Sub(f.From) > MaxEventRange {
		return nil, nil, ErrRange
	}
	if db == nil {
		return []Event{}, nil, nil
	}
	var q strings.Builder
	q.WriteString(`SELECT ` + strings.Join(EventColumns, ", ") + ` FROM ` + EventsTable + `
		WHERE {tenant} AND ts >= fromUnixTimestamp64Milli(?) AND ts < fromUnixTimestamp64Milli(?)`)
	args := []any{f.From.UnixMilli(), f.To.UnixMilli()}
	if after != nil {
		q.WriteString(` AND (ts, trace, detector, location) < (fromUnixTimestamp64Milli(?), ?, ?, ?)`)
		args = append(args, after.TS.UnixMilli(), after.Trace, after.Detector, after.Location)
	}
	for _, c := range []struct{ column, value string }{
		{"detector", f.Detector}, {"class", f.Class}, {"action", f.Action}, {"location", f.Location}, {"trace", f.Trace},
	} {
		if c.value != "" {
			q.WriteString(" AND " + c.column + " = ?")
			args = append(args, c.value)
		}
	}
	if f.Key != uuid.Nil {
		q.WriteString(" AND key_id = ?")
		args = append(args, f.Key)
	}
	if f.Blocked {
		q.WriteString(" AND blocked")
	}
	q.WriteString(" ORDER BY ts DESC, trace DESC, detector DESC, location DESC LIMIT ?")
	args = append(args, limit+1)
	var out []Event
	if err := db.Select(ctx, &out, events.Project(project), q.String(), args...); err != nil {
		return nil, nil, fmt.Errorf("firewall: events: %w", err)
	}
	var next *EventCursor
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = &EventCursor{TS: last.TS, Trace: last.Trace, Detector: last.Detector, Location: last.Location}
	}
	if out == nil {
		out = []Event{}
	}
	return out, next, nil
}

// Stats are what the firewall did over a time range.
type Stats struct {
	// Calls it found something in or a policy decided; Blocked those it
	// refused.
	Calls, Blocked uint64
	Actions        []ActionCount
	Detectors      []DetectorCount
	Days           []DayCount
}

// ActionCount is the calls and findings an action took.
type ActionCount struct {
	Action   string `ch:"action"`
	Calls    uint64 `ch:"calls"`
	Findings uint64 `ch:"findings"`
}

// DetectorCount is a detector's calls and findings.
type DetectorCount struct {
	Detector string `ch:"detector"`
	Class    string `ch:"class"`
	Calls    uint64 `ch:"calls"`
	Findings uint64 `ch:"findings"`
}

// DayCount is a day's calls and blocked calls (UTC).
type DayCount struct {
	Day     time.Time `ch:"day"`
	Calls   uint64    `ch:"calls"`
	Blocked uint64    `ch:"blocked"`
}

// ReadStats reads a project's stats over a time range: the 50 detectors
// with the most findings.
func ReadStats(ctx context.Context, db *events.DB, project uuid.UUID, from, to time.Time) (Stats, error) {
	if from.IsZero() || !from.Before(to) || to.Sub(from) > MaxStatsRange {
		return Stats{}, ErrRange
	}
	out := Stats{Actions: []ActionCount{}, Detectors: []DetectorCount{}, Days: []DayCount{}}
	if db == nil {
		return out, nil
	}
	scope := events.Project(project)
	where := ` FROM ` + EventsTable + ` WHERE {tenant} AND ts >= fromUnixTimestamp64Milli(?) AND ts < fromUnixTimestamp64Milli(?)`
	args := []any{from.UnixMilli(), to.UnixMilli()}
	var totals []struct {
		Calls   uint64 `ch:"calls"`
		Blocked uint64 `ch:"blocked"`
	}
	if err := db.Select(ctx, &totals, scope, `SELECT uniqExact(trace) AS calls, uniqExactIf(trace, blocked) AS blocked`+where, args...); err != nil {
		return Stats{}, fmt.Errorf("firewall: stats: %w", err)
	}
	if len(totals) == 1 {
		out.Calls, out.Blocked = totals[0].Calls, totals[0].Blocked
	}
	if err := db.Select(ctx, &out.Actions, scope, `SELECT action, uniqExact(trace) AS calls, sum(count) AS findings`+where+
		` GROUP BY action ORDER BY calls DESC`, args...); err != nil {
		return Stats{}, fmt.Errorf("firewall: stats: %w", err)
	}
	if err := db.Select(ctx, &out.Detectors, scope, `SELECT detector, any(class) AS class, uniqExact(trace) AS calls,
		sum(count) AS findings`+where+` AND detector != '' GROUP BY detector ORDER BY findings DESC, detector LIMIT 50`, args...); err != nil {
		return Stats{}, fmt.Errorf("firewall: stats: %w", err)
	}
	if err := db.Select(ctx, &out.Days, scope, `SELECT toStartOfDay(ts) AS day, uniqExact(trace) AS calls,
		uniqExactIf(trace, blocked) AS blocked`+where+` GROUP BY day ORDER BY day`, args...); err != nil {
		return Stats{}, fmt.Errorf("firewall: stats: %w", err)
	}
	return out, nil
}
