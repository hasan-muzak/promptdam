// Package training trains model routers' classifiers in the worker: from
// each router's judged calls of the last 30 days (router_evals), one model
// per tier but the default, calibrated to the router's quality floor
// (features/classifier). Gateways get a new classifier with the project's
// configuration within seconds.
package training

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/jobs"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/modelrouter"
	"github.com/hasan-muzak/promptdam/server/ent/routerclassifier"
	"github.com/hasan-muzak/promptdam/server/features/classifier"
	"github.com/hasan-muzak/promptdam/server/features/evaluate"
)

// Window is how far back judged calls are read.
const Window = 30 * 24 * time.Hour

// maxPerTier bounds the judged calls a tier is trained on: the newest.
const maxPerTier = 20_000

// Config configures a Service.
type Config struct {
	Ent    *pdent.Client
	Events *events.DB
	// Changed runs after a router's classifier changes, so gateways reload
	// the project's configuration.
	Changed func(ctx context.Context, project uuid.UUID)
	Now     func() time.Time
}

// Service trains classifiers.
type Service struct{ cfg Config }

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Changed == nil {
		cfg.Changed = func(context.Context, uuid.UUID) {}
	}
	return &Service{cfg: cfg}
}

// ErrNoEvents is returned without ClickHouse: there is nothing to train on.
var ErrNoEvents = errors.New("training: no request log")

type judged struct {
	Tier     string   `ch:"candidate_tier"`
	Verdict  string   `ch:"verdict"`
	Features []uint32 `ch:"features"`
}

// Train trains a router's classifier and stores it. ctx is scoped to the
// router's organization.
func (s *Service) Train(ctx context.Context, r *pdent.ModelRouter) (*classifier.Model, error) {
	if s.cfg.Events == nil {
		return nil, ErrNoEvents
	}
	var rows []judged
	// Only calls judged against the router's current default tier say
	// anything of its tiers now.
	err := s.cfg.Events.Select(ctx, &rows, events.Project(r.ProjectID), `SELECT candidate_tier, verdict, features
		FROM router_evals WHERE {tenant} AND router = ? AND baseline_tier = ? AND verdict != ''
		AND ts >= fromUnixTimestamp64Milli(?) ORDER BY ts DESC LIMIT ?`, r.Name, r.DefaultTier, s.cfg.Now().Add(-Window).UnixMilli(),
		maxPerTier*len(r.Tiers))
	if err != nil {
		return nil, fmt.Errorf("training: reading judged calls: %w", err)
	}
	def := -1
	for i, t := range r.Tiers {
		if t.Name == r.DefaultTier {
			def = i
		}
	}
	byTier := map[string][]classifier.Sample{}
	for _, row := range rows {
		if len(byTier[row.Tier]) >= maxPerTier {
			continue
		}
		byTier[row.Tier] = append(byTier[row.Tier], classifier.Sample{Features: row.Features})
		s := &byTier[row.Tier][len(byTier[row.Tier])-1]
		s.Y = row.Verdict == evaluate.Win
		if rank := rankOf(r, row.Tier, def); rank < 0 {
			s.Y = s.Y || row.Verdict == evaluate.Tie // a cheaper tier holds up when it ties
		}
	}
	m := &classifier.Model{TrainedAt: s.cfg.Now().UTC(), Default: r.DefaultTier}
	for i, t := range r.Tiers {
		if i == def {
			continue
		}
		m.Tiers = append(m.Tiers, classifier.TrainTier(t.Name, i-def, byTier[t.Name],
			classifier.Options{Floor: r.QualityFloor, Seed: uint64(r.ID.ID())}))
	}
	prev, err := s.cfg.Ent.RouterClassifier.Query().Where(routerclassifier.RouterID(r.ID)).Only(ctx)
	switch {
	case pdent.IsNotFound(err):
		m.Version = 1
		err = s.cfg.Ent.RouterClassifier.Create().SetOrgID(r.OrgID).SetProjectID(r.ProjectID).SetRouterID(r.ID).SetModel(*m).
			SetSamples(len(rows)).SetTrainedAt(m.TrainedAt).Exec(ctx)
	case err == nil:
		m.Version = prev.Model.Version + 1
		err = s.cfg.Ent.RouterClassifier.UpdateOne(prev).SetModel(*m).SetSamples(len(rows)).SetTrainedAt(m.TrainedAt).Exec(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("training: storing the classifier: %w", err)
	}
	s.cfg.Changed(ctx, r.ProjectID)
	return m, nil
}

func rankOf(r *pdent.ModelRouter, tier string, def int) int {
	for i, t := range r.Tiers {
		if t.Name == tier {
			return i - def
		}
	}
	return 0
}

// TrainDue trains every router with calls judged since its classifier was
// trained (or without a classifier yet). It runs hourly.
func (s *Service) TrainDue(ctx context.Context) error {
	if s.cfg.Events == nil {
		return nil
	}
	ctx = entx.SkipTenant(ctx)
	var latest []struct {
		Project uuid.UUID `ch:"project_id"`
		Router  string    `ch:"router"`
		Last    time.Time `ch:"last"`
	}
	if err := s.cfg.Events.Unscoped().Select(ctx, &latest, `SELECT project_id, router, max(ts) AS last FROM router_evals
		WHERE verdict != '' AND ts >= fromUnixTimestamp64Milli(?) GROUP BY project_id, router`, s.cfg.Now().Add(-Window).UnixMilli()); err != nil {
		return fmt.Errorf("training: finding routers to train: %w", err)
	}
	var errs []error
	for _, l := range latest {
		r, err := s.cfg.Ent.ModelRouter.Query().Where(modelrouter.ProjectID(l.Project), modelrouter.Name(l.Router),
			modelrouter.StatusNEQ(modelrouter.StatusDisabled)).Only(ctx)
		if pdent.IsNotFound(err) {
			continue // deleted or disabled since
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		c, err := s.cfg.Ent.RouterClassifier.Query().Where(routerclassifier.RouterID(r.ID)).Only(ctx)
		if err == nil && !l.Last.After(c.TrainedAt) {
			continue
		}
		if _, err := s.Train(entx.WithOrg(ctx, r.OrgID), r); err != nil {
			errs = append(errs, err)
			continue
		}
		slog.Info("training: a router's classifier was trained", "router", r.ID)
	}
	return errors.Join(errs...)
}

// DueArgs trains the routers due (hourly).
type DueArgs struct{}

// Kind implements river.JobArgs.
func (DueArgs) Kind() string { return "promptdam_routers_train" }

// RouterArgs trains one router now (the routers API's train request).
type RouterArgs struct {
	Org    uuid.UUID `json:"org"`
	Router uuid.UUID `json:"router"`
}

// Kind implements river.JobArgs.
func (RouterArgs) Kind() string { return "promptdam_router_train" }

type dueWorker struct {
	river.WorkerDefaults[DueArgs]
	s *Service
}

func (w *dueWorker) Work(ctx context.Context, _ *river.Job[DueArgs]) error { return w.s.TrainDue(ctx) }

type routerWorker struct {
	river.WorkerDefaults[RouterArgs]
	s *Service
}

func (w *routerWorker) Work(ctx context.Context, job *river.Job[RouterArgs]) error {
	ctx = entx.WithOrg(ctx, job.Args.Org)
	r, err := w.s.cfg.Ent.ModelRouter.Get(ctx, job.Args.Router)
	if pdent.IsNotFound(err) {
		return nil // deleted since
	}
	if err != nil {
		return err
	}
	_, err = w.s.Train(ctx, r)
	return err
}

// RegisterWorkers registers the training jobs; the routers due are trained
// hourly.
func RegisterWorkers(j *jobs.Jobs, s *Service) {
	jobs.Register(j, &dueWorker{s: s})
	jobs.Register(j, &routerWorker{s: s})
	j.Every("promptdam-routers-train", time.Hour, func() river.JobArgs { return DueArgs{} })
}
