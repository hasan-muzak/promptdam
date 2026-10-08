// Package activity keeps what happened to each project that was worth
// telling: budget thresholds, ready routers and demoted tiers, calls the
// firewall refused, alert rules firing. Those who tell (the budgets, the
// routers' and the firewall's watchers, alert rules) record an entry next
// to their alerts and webhooks; the dashboard reads them as a feed, and a
// budget's or a router's own history by its subject. Entries live in
// ClickHouse, kept 90 days by its TTL: they come as often as a minute per
// project.
package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/ids"
)

// IDPrefix starts activity IDs at the API: act_….
const IDPrefix = "act"

// Table holds the entries.
const Table = "activities"

// Retention is how long entries are kept (the table's TTL).
const Retention = 90 * 24 * time.Hour

// Severities.
const (
	Info     = "info"
	Warning  = "warning"
	Critical = "critical"
)

// Entry is one thing that happened.
type Entry struct {
	Org, Project uuid.UUID
	// Kind names it as its webhook event does (budget.exceeded…).
	Kind, Severity, Title, Body string
	// Subject is what it is about, as the API names it (bdg_…, rtr_…).
	Subject, URL string
	Data         map[string]any
}

// Row is an entry as kept.
type Row struct {
	ID       uuid.UUID `ch:"id"`
	TS       time.Time `ch:"ts"`
	Kind     string    `ch:"kind"`
	Severity string    `ch:"severity"`
	Title    string    `ch:"title"`
	Body     string    `ch:"body"`
	Subject  string    `ch:"subject"`
	URL      string    `ch:"url"`
	DataJSON string    `ch:"data"`
}

// Data is the entry's numbers.
func (r Row) Data() map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(r.DataJSON), &out)
	return out
}

// Service keeps entries.
type Service struct {
	db  *events.DB
	now func() time.Time
}

// New returns a Service; without ClickHouse it keeps nothing.
func New(db *events.DB) *Service { return &Service{db: db, now: time.Now} }

// Record keeps an entry. It never fails who tells: an entry that can't be
// kept is logged, the alert it goes with has gone out anyway.
func (s *Service) Record(ctx context.Context, e Entry) {
	if s == nil || s.db == nil {
		return
	}
	if e.Severity == "" {
		e.Severity = Info
	}
	data, err := json.Marshal(e.Data)
	if err != nil || e.Data == nil {
		data = []byte("{}")
	}
	err = s.db.Unscoped().AsyncInsert(context.WithoutCancel(ctx), `INSERT INTO `+Table+` (project_id, ts, id, org_id, kind,
		severity, title, body, subject, url, data) VALUES (?, fromUnixTimestamp64Milli(?), ?, ?, ?, ?, ?, ?, ?, ?, ?)`, true,
		e.Project, s.now().UnixMilli(), ids.New(), e.Org, e.Kind, e.Severity, truncate(e.Title, 300), truncate(e.Body, 4000),
		e.Subject, truncate(e.URL, 500), string(data))
	if err != nil {
		slog.Warn("activity: an entry wasn't kept", "kind", e.Kind, "project", e.Project, "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// Filter narrows a project's entries.
type Filter struct {
	Kind, Subject string
}

// Cursor is where a page of entries ends.
type Cursor struct {
	TS time.Time
	ID uuid.UUID
}

// List returns up to limit entries of a project, newest first, after the
// cursor; next is set when more follow.
func (s *Service) List(ctx context.Context, project uuid.UUID, f Filter, after *Cursor, limit int) ([]Row, *Cursor, error) {
	if s == nil || s.db == nil {
		return []Row{}, nil, nil
	}
	var q strings.Builder
	q.WriteString(`SELECT id, ts, kind, severity, title, body, subject, url, data FROM ` + Table + ` WHERE {tenant}`)
	var args []any
	if f.Kind != "" {
		q.WriteString(" AND kind = ?")
		args = append(args, f.Kind)
	}
	if f.Subject != "" {
		q.WriteString(" AND subject = ?")
		args = append(args, f.Subject)
	}
	if after != nil {
		q.WriteString(" AND (ts, id) < (fromUnixTimestamp64Milli(?), ?)")
		args = append(args, after.TS.UnixMilli(), after.ID)
	}
	q.WriteString(" ORDER BY ts DESC, id DESC LIMIT ?")
	args = append(args, limit+1)
	var rows []Row
	if err := s.db.Select(ctx, &rows, events.Project(project), q.String(), args...); err != nil {
		return nil, nil, fmt.Errorf("activity: %w", err)
	}
	var next *Cursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = &Cursor{TS: last.TS, ID: last.ID}
	}
	if rows == nil {
		rows = []Row{}
	}
	return rows, next, nil
}
