// Package activity keeps what happened to each project that was worth
// telling: budget thresholds, ready routers and demoted tiers, calls the
// firewall refused, alert rules firing. Those who tell (the budgets, the
// routers' and the firewall's watchers, alert rules) record an entry next
// to their alerts and webhooks; the dashboard reads them as a feed, and a
// budget's or a router's own history by its subject. Entries are kept 90
// days.
package activity

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/jobs"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/activity"
)

// IDPrefix starts activity IDs at the API: act_….
const IDPrefix = "act"

// Retention is how long entries are kept.
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

// Service keeps entries.
type Service struct {
	ent *pdent.Client
}

// New returns a Service.
func New(ent *pdent.Client) *Service { return &Service{ent: ent} }

// Record keeps an entry. It never fails who tells: an entry that can't be
// kept is logged, the alert it goes with has gone out anyway.
func (s *Service) Record(ctx context.Context, e Entry) {
	if s == nil {
		return
	}
	if e.Severity == "" {
		e.Severity = Info
	}
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	err := s.ent.Activity.Create().SetOrgID(e.Org).SetProjectID(e.Project).SetKind(e.Kind).
		SetSeverity(activity.Severity(e.Severity)).SetTitle(truncate(e.Title, 300)).SetBody(truncate(e.Body, 4000)).
		SetSubject(e.Subject).SetURL(truncate(e.URL, 500)).SetData(e.Data).
		Exec(entx.WithOrg(ctx, e.Org))
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

// List returns up to limit entries of a project, newest first, after the
// entry after (uuid.Nil: from the newest); next is set when more follow.
func (s *Service) List(ctx context.Context, project uuid.UUID, f Filter, after uuid.UUID, limit int) (
	[]*pdent.Activity, uuid.UUID, error) {
	q := s.ent.Activity.Query().Where(activity.ProjectID(project))
	if f.Kind != "" {
		q.Where(activity.Kind(f.Kind))
	}
	if f.Subject != "" {
		q.Where(activity.Subject(f.Subject))
	}
	if after != uuid.Nil {
		q.Where(activity.IDLT(after)) // IDs are time-ordered
	}
	rows, err := q.Order(pdent.Desc(activity.FieldID)).Limit(limit + 1).All(ctx)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("activity: %w", err)
	}
	var next uuid.UUID
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID
	}
	return rows, next, nil
}

// Prune deletes the entries older than the retention.
func (s *Service) Prune(ctx context.Context, now time.Time) (int, error) {
	return s.ent.Activity.Delete().Where(activity.CreatedAtLT(now.Add(-Retention))).Exec(entx.SkipTenant(ctx))
}

// PruneArgs prunes old entries (daily).
type PruneArgs struct{}

// Kind implements river.JobArgs.
func (PruneArgs) Kind() string { return "promptdam_activity_prune" }

type pruneWorker struct {
	river.WorkerDefaults[PruneArgs]
	s *Service
}

func (w *pruneWorker) Work(ctx context.Context, _ *river.Job[PruneArgs]) error {
	_, err := w.s.Prune(ctx, time.Now())
	return err
}

// RegisterWorkers prunes old entries daily.
func RegisterWorkers(j *jobs.Jobs, s *Service) {
	jobs.Register(j, &pruneWorker{s: s})
	j.Every("promptdam-activity-prune", 24*time.Hour, func() river.JobArgs { return PruneArgs{} })
}
