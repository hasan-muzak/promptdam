// Package cachepolicy learns, in the worker, how long Anthropic's prompt
// cache keeps each prompt family's prefix and conversation tails (five
// minutes or an hour, features/cachettl), from the gaps between their calls
// over the last days, and ships the decisions to gateways with the
// project's configuration. It also counts the calls that wrote a prefix at
// the same moment as another, for the cache doctor.
package cachepolicy

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/llm"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/cachepolicy"
	"github.com/hasan-muzak/promptdam/server/features/cachettl"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
)

// Window is how far back gaps are weighed.
const Window = 3 * 24 * time.Hour

// MaxFamilies bounds the families a project's policy keeps: those that
// cache the most.
const MaxFamilies = 256

// coldWindow is how close after another call a call that wrote the same
// prefix without reading it counts as sent together (cold parallel).
const coldWindow = 10 * time.Second

// Config configures a Service.
type Config struct {
	Ent    *pdent.Client
	Events *events.DB
	Prices prices.Prices
	// Changed runs after a project's lifetimes change, so gateways reload
	// its configuration.
	Changed func(ctx context.Context, project uuid.UUID)
	Now     func() time.Time
}

// Service learns cache lifetimes.
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

// Serving is a project's families' lifetimes where one is kept an hour, for
// the gateway's snapshot. ctx is scoped to the project's organization.
func (s *Service) Serving(ctx context.Context, project uuid.UUID) (map[string]cachettl.TTL, error) {
	fams, err := s.Families(ctx, project)
	if err != nil {
		return nil, err
	}
	out := map[string]cachettl.TTL{}
	for name, f := range fams {
		if t := f.TTLOf(); t != (cachettl.TTL{}) {
			out[name] = t
		}
	}
	return out, nil
}

// Families is what was learned of a project's families (none before the
// first learning).
func (s *Service) Families(ctx context.Context, project uuid.UUID) (map[string]cachettl.Family, error) {
	p, err := s.cfg.Ent.CachePolicy.Query().Where(cachepolicy.ProjectID(project)).Only(ctx)
	switch {
	case pdent.IsNotFound(err):
		return map[string]cachettl.Family{}, nil
	case err != nil:
		return nil, fmt.Errorf("cachepolicy: %w", err)
	}
	return p.Families, nil
}

type gapRow struct {
	Family   string `ch:"family_id"`
	Model    string `ch:"served_model"`
	Kind     string `ch:"kind"`
	Calls    uint64 `ch:"calls"`
	MidCalls uint64 `ch:"mid_calls"`
	Mid      uint64 `ch:"mid"`
	Inc      uint64 `ch:"inc"`
	Long     uint64 `ch:"long"`
}

type prefixRow struct {
	Family     string `ch:"family_id"`
	MidCalls   uint64 `ch:"mid_calls"`
	Mid        uint64 `ch:"mid"`
	Long       uint64 `ch:"long"`
	Cold       uint64 `ch:"cold"`
	ColdTokens uint64 `ch:"cold_tokens"`
}

// Learn weighs a project's families and stores their lifetimes; gateways
// hear of it when a lifetime changed. ctx is scoped to the project's
// organization.
func (s *Service) Learn(ctx context.Context, org, project uuid.UUID) error {
	if s.cfg.Events == nil {
		return nil
	}
	since := s.cfg.Now().Add(-Window)
	var gaps []gapRow
	if err := s.cfg.Events.Select(ctx, &gaps, events.Project(project), `SELECT family_id, served_model, kind,
		sum(calls) AS calls, sum(mid_calls) AS mid_calls, sum(mid_tokens) AS mid, sum(inc_tokens) AS inc, sum(long_tokens) AS long
		FROM cache_gaps_hourly WHERE {tenant} AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?))
		GROUP BY family_id, served_model, kind`, since.UnixMilli()); err != nil {
		return fmt.Errorf("cachepolicy: reading the tails' gaps: %w", err)
	}
	// Calls sharing a prefix, in prefix order (the table's): the gap to the
	// previous call with the same prefix, whatever its conversation.
	var pre []prefixRow
	if err := s.cfg.Events.Select(ctx, &pre, events.Project(project), `SELECT family_id,
		countIf(n > 1 AND gap >= 300 AND gap < 3600) AS mid_calls,
		sumIf(toUInt64(prefix_tokens), n > 1 AND gap >= 300 AND gap < 3600) AS mid,
		sumIf(toUInt64(prefix_tokens), n = 1 OR gap >= 3600) AS long,
		countIf(n > 1 AND gap_ms < ? AND cache_read_tokens = 0 AND cache_write_tokens > 0) AS cold,
		sumIf(toUInt64(cache_write_tokens), n > 1 AND gap_ms < ? AND cache_read_tokens = 0 AND cache_write_tokens > 0) AS cold_tokens
		FROM (SELECT family_id, prefix_tokens, cache_read_tokens, cache_write_tokens,
			row_number() OVER w AS n,
			dateDiff('millisecond', lagInFrame(ts) OVER w, ts) AS gap_ms, intDiv(gap_ms, 1000) AS gap
			FROM prefix_starts WHERE {tenant} AND ts >= fromUnixTimestamp64Milli(?)
			WINDOW w AS (PARTITION BY prefix_hash ORDER BY ts ROWS BETWEEN 1 PRECEDING AND CURRENT ROW))
		GROUP BY family_id`, coldWindow.Milliseconds(), coldWindow.Milliseconds(), since.UnixMilli()); err != nil {
		return fmt.Errorf("cachepolicy: reading the prefixes' gaps: %w", err)
	}
	prev, err := s.Families(ctx, project)
	if err != nil {
		return err
	}
	next := weigh(gaps, pre, prev, s.price)
	changed := !maps.EqualFunc(servingOf(prev), servingOf(next), func(a, b cachettl.TTL) bool { return a == b })
	if err := s.store(ctx, org, project, next); err != nil {
		return err
	}
	if changed {
		s.cfg.Changed(ctx, project)
	}
	return nil
}

// weigh decides each family's lifetimes from its gaps, at the prices of
// the model most of its calls used; the families that cache the most are
// kept.
func weigh(gaps []gapRow, pre []prefixRow, prev map[string]cachettl.Family, price func(kind, model string) (llm.Model, bool)) map[string]cachettl.Family {
	type acc struct {
		gaps       cachettl.Gaps
		model      string
		kind       string
		modelCalls uint64
		weight     uint64
	}
	byFamily := map[string]*acc{}
	for _, g := range gaps {
		a := byFamily[g.Family]
		if a == nil {
			a = &acc{}
			byFamily[g.Family] = a
		}
		a.gaps.Mid, a.gaps.Inc, a.gaps.Long = a.gaps.Mid+int64(g.Mid), a.gaps.Inc+int64(g.Inc), a.gaps.Long+int64(g.Long)
		a.gaps.MidCalls += int64(g.MidCalls)
		a.weight += g.Mid + g.Inc + g.Long
		if g.Calls > a.modelCalls {
			a.model, a.kind, a.modelCalls = g.Model, g.Kind, g.Calls
		}
	}
	prefixes := map[string]prefixRow{}
	for _, p := range pre {
		prefixes[p.Family] = p
	}
	names := slices.Collect(maps.Keys(byFamily))
	slices.SortFunc(names, func(a, b string) int { return cmp.Compare(byFamily[b].weight, byFamily[a].weight) })
	out := make(map[string]cachettl.Family, min(len(names), MaxFamilies))
	for _, name := range names[:min(len(names), MaxFamilies)] {
		a := byFamily[name]
		m, ok := price(a.kind, a.model)
		if !ok {
			continue
		}
		p := prefixes[name]
		f := cachettl.Family{Model: a.model, TailGaps: a.gaps,
			PrefixGaps: cachettl.Gaps{Mid: int64(p.Mid), Long: int64(p.Long), MidCalls: int64(p.MidCalls)}, ColdParallel: int64(p.Cold)}
		was := prev[name]
		f.Tail = cachettl.Decide(f.TailGaps, m, was.Tail.TTL)
		f.Prefix = cachettl.Decide(f.PrefixGaps, m, was.Prefix.TTL)
		if write, read := m.CacheWrite, m.CachedInput; write > read && read > 0 {
			f.ColdParallelMicros = int64(math.Round(float64(p.ColdTokens) * (write - read)))
		}
		out[name] = f
	}
	return out
}

func servingOf(fams map[string]cachettl.Family) map[string]cachettl.TTL {
	out := map[string]cachettl.TTL{}
	for name, f := range fams {
		if t := f.TTLOf(); t != (cachettl.TTL{}) {
			out[name] = t
		}
	}
	return out
}

func (s *Service) price(kind, model string) (llm.Model, bool) {
	catalogName := ""
	if k, ok := providers.LookupKind(kind); ok {
		catalogName = k.Catalog
	}
	return s.cfg.Prices.Current().Lookup(catalogName, model)
}

func (s *Service) store(ctx context.Context, org, project uuid.UUID, fams map[string]cachettl.Family) error {
	now := s.cfg.Now().UTC()
	p, err := s.cfg.Ent.CachePolicy.Query().Where(cachepolicy.ProjectID(project)).Only(ctx)
	switch {
	case pdent.IsNotFound(err):
		err = s.cfg.Ent.CachePolicy.Create().SetOrgID(org).SetProjectID(project).SetFamilies(fams).SetLearnedAt(now).Exec(ctx)
	case err == nil:
		err = s.cfg.Ent.CachePolicy.UpdateOne(p).SetFamilies(fams).SetLearnedAt(now).Exec(ctx)
	}
	if err != nil {
		return fmt.Errorf("cachepolicy: storing: %w", err)
	}
	return nil
}

// LearnDue learns every project that cached on Anthropic's API lately. It
// runs every six hours.
func (s *Service) LearnDue(ctx context.Context) error {
	if s.cfg.Events == nil {
		return nil
	}
	var projects []struct {
		Project uuid.UUID `ch:"project_id"`
	}
	if err := s.cfg.Events.Unscoped().Select(ctx, &projects, `SELECT DISTINCT project_id FROM cache_gaps_hourly
		WHERE hour >= toStartOfHour(fromUnixTimestamp64Milli(?))`, s.cfg.Now().Add(-Window).UnixMilli()); err != nil {
		return fmt.Errorf("cachepolicy: finding projects: %w", err)
	}
	var errs []error
	for _, p := range projects {
		org, err := s.orgOf(ctx, p.Project)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.Learn(entx.WithOrg(ctx, org), org, p.Project); err != nil {
			errs = append(errs, err)
			continue
		}
		slog.Debug("cachepolicy: a project's cache lifetimes were learned", "project", p.Project)
	}
	return errors.Join(errs...)
}

// orgOf is a project's organization, read from its ClickHouse rows (the
// request log carries it).
func (s *Service) orgOf(ctx context.Context, project uuid.UUID) (uuid.UUID, error) {
	var rows []struct {
		Org uuid.UUID `ch:"org_id"`
	}
	if err := s.cfg.Events.Select(ctx, &rows, events.Project(project), `SELECT org_id FROM requests WHERE {tenant}
		ORDER BY ts DESC LIMIT 1`); err != nil || len(rows) == 0 {
		return uuid.Nil, fmt.Errorf("cachepolicy: the organization of project %s: %w", project, err)
	}
	return rows[0].Org, nil
}

// DueArgs learns the projects due.
type DueArgs struct{}

// Kind implements river.JobArgs.
func (DueArgs) Kind() string { return "promptdam_cache_ttl" }

type dueWorker struct {
	river.WorkerDefaults[DueArgs]
	s *Service
}

func (w *dueWorker) Work(ctx context.Context, _ *river.Job[DueArgs]) error { return w.s.LearnDue(ctx) }

// RegisterWorkers registers the learning job, every six hours.
func RegisterWorkers(j *jobs.Jobs, s *Service) {
	jobs.Register(j, &dueWorker{s: s})
	j.Every("promptdam-cache-ttl", 6*time.Hour, func() river.JobArgs { return DueArgs{} })
}
