// Package purge deletes everything Promptdam keeps of a deleted
// organization or project, physically: nothing is kept to restore it from.
//
// Its Postgres rows go in the deletion's own transaction (tenancy's
// OnOrgDeleted and OnProjectDeleted hooks). The same transaction enqueues
// two purge jobs for the rest: ClickHouse rows and Valkey keys. The first
// runs at once and is retried until everything is gone; the final one runs
// LateWrites later and removes what gateways wrote meanwhile from the
// configuration they still held.
package purge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/kv"
	"github.com/hasan-muzak/keel-core/pkg/kvcache"
	"github.com/hasan-muzak/keel-core/pkg/metering"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/ratelimit"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/activity"
	"github.com/hasan-muzak/promptdam/server/ent/budget"
	"github.com/hasan-muzak/promptdam/server/ent/familylabel"
	"github.com/hasan-muzak/promptdam/server/ent/firewallsetting"
	"github.com/hasan-muzak/promptdam/server/ent/keysetting"
	"github.com/hasan-muzak/promptdam/server/ent/modelrouter"
	"github.com/hasan-muzak/promptdam/server/ent/priceoverride"
	"github.com/hasan-muzak/promptdam/server/ent/provider"
	"github.com/hasan-muzak/promptdam/server/ent/route"
	"github.com/hasan-muzak/promptdam/server/ent/routerclassifier"
	"github.com/hasan-muzak/promptdam/server/ent/routingrule"
)

// LateWrites is how long after a deletion its data can still be written,
// and so how long the final run waits: gateways keep serving a deleted
// project's keys and configuration for at most a few minutes, and its
// request log reaches ClickHouse within seconds of each call.
const LateWrites = 10 * time.Minute

const (
	// eraseWait is how long a run waits for ClickHouse's deletions before
	// it snoozes (without counting an attempt) and looks again.
	eraseWait = time.Minute
	// maxAttempts retries a failing purge hourly for about a month.
	maxAttempts = 24 * 30
)

// Config configures a Service. Events is nil when Promptdam runs without
// ClickHouse.
type Config struct {
	Ent      *pdent.Client
	Jobs     *jobs.Jobs
	Events   *events.DB
	KV       *kv.Store
	Metering *metering.Service
}

// Service deletes deleted organizations' and projects' data.
type Service struct {
	cfg Config
}

// New returns the service.
func New(cfg Config) *Service { return &Service{cfg: cfg} }

// Args names the data a purge removes: an organization's (Whole) or some of
// its projects'.
type Args struct {
	Org      uuid.UUID   `json:"org"`
	Whole    bool        `json:"whole,omitempty"`
	Projects []uuid.UUID `json:"projects"`
	// Final marks the run that removes what was written after the deletion.
	Final bool `json:"final,omitempty"`
}

// Kind implements river.JobArgs.
func (Args) Kind() string { return "promptdam_purge" }

// OrgDeleted is tenancy's OnOrgDeleted hook: in the deletion's transaction,
// it deletes the organization's Promptdam rows and enqueues their purge.
func (s *Service) OrgDeleted(ctx context.Context, d tenancy.OrgDeletion) error {
	ctx = entx.WithOrg(ctx, d.Org)
	if err := deleteRows(ctx, txClient(d.Tx), d.Org, nil); err != nil {
		return err
	}
	return s.enqueue(ctx, d.Tx, Args{Org: d.Org, Whole: true, Projects: d.Projects})
}

// ProjectDeleted is tenancy's OnProjectDeleted hook: in the deletion's
// transaction, it deletes the project's Promptdam rows and enqueues their
// purge.
func (s *Service) ProjectDeleted(ctx context.Context, d tenancy.ProjectDeletion) error {
	ctx = entx.WithOrg(ctx, d.Org)
	if err := deleteRows(ctx, txClient(d.Tx), d.Org, []uuid.UUID{d.Project}); err != nil {
		return err
	}
	return s.enqueue(ctx, d.Tx, Args{Org: d.Org, Projects: []uuid.UUID{d.Project}})
}

func (s *Service) enqueue(ctx context.Context, tx *sql.Tx, a Args) error {
	if err := s.cfg.Jobs.EnqueueTx(ctx, tx, a, &river.InsertOpts{MaxAttempts: maxAttempts}); err != nil {
		return fmt.Errorf("purge: enqueueing: %w", err)
	}
	a.Final = true
	err := s.cfg.Jobs.EnqueueTx(ctx, tx, a, &river.InsertOpts{MaxAttempts: maxAttempts, ScheduledAt: time.Now().Add(LateWrites)})
	if err != nil {
		return fmt.Errorf("purge: enqueueing the final run: %w", err)
	}
	return nil
}

// Purge removes the data a names from Postgres (rows written since the
// deletion), Valkey and ClickHouse. Every step can run again. It returns
// events.ErrErasing while ClickHouse is still deleting.
func (s *Service) Purge(ctx context.Context, a Args) error {
	ctx = entx.WithOrg(ctx, a.Org)
	projects := a.Projects
	if a.Whole {
		projects = nil
	}
	if err := deleteRows(ctx, s.cfg.Ent, a.Org, projects); err != nil {
		return fmt.Errorf("purge: rows: %w", err)
	}
	if a.Whole && s.cfg.Metering != nil {
		if err := s.cfg.Metering.ForgetOrg(ctx, a.Org); err != nil {
			return fmt.Errorf("purge: usage counters: %w", err)
		}
	}
	if _, err := s.cfg.KV.DeleteMatching(ctx, keysOf(a).match); err != nil {
		return fmt.Errorf("purge: valkey: %w", err)
	}
	if s.cfg.Events == nil || len(a.Projects) == 0 {
		return nil
	}
	return s.cfg.Events.Erase(ctx, events.Project(a.Projects...), eraseWait)
}

// txClient is Promptdam's graph in a deletion's transaction.
func txClient(tx *sql.Tx) *pdent.Client {
	return pdent.NewClient(pdent.Driver(entsql.NewDriver(dialect.Postgres, entsql.Conn{ExecQuerier: tx})))
}

// deleter deletes rows of one entity.
type deleter interface {
	Exec(ctx context.Context) (int, error)
}

// deleteRows deletes the rows of every entity of the graph (a test holds
// this list to the graph): the organization's, or only those of projects
// when given. ctx is scoped to the organization (entx.WithOrg).
func deleteRows(ctx context.Context, c *pdent.Client, org uuid.UUID, projects []uuid.UUID) error {
	scoped := len(projects) > 0
	for _, d := range []deleter{
		c.Activity.Delete().Where(activity.OrgID(org), only(scoped, activity.ProjectIDIn(projects...))),
		c.Budget.Delete().Where(budget.OrgID(org), only(scoped, budget.ProjectIDIn(projects...))),
		c.FamilyLabel.Delete().Where(familylabel.OrgID(org), only(scoped, familylabel.ProjectIDIn(projects...))),
		c.FirewallSetting.Delete().Where(firewallsetting.OrgID(org), only(scoped, firewallsetting.ProjectIDIn(projects...))),
		c.KeySetting.Delete().Where(keysetting.OrgID(org), only(scoped, keysetting.ProjectIDIn(projects...))),
		c.ModelRouter.Delete().Where(modelrouter.OrgID(org), only(scoped, modelrouter.ProjectIDIn(projects...))),
		c.RouterClassifier.Delete().Where(routerclassifier.OrgID(org), only(scoped, routerclassifier.ProjectIDIn(projects...))),
		c.PriceOverride.Delete().Where(priceoverride.OrgID(org), only(scoped, priceoverride.ProjectIDIn(projects...))),
		c.Provider.Delete().Where(provider.OrgID(org), only(scoped, provider.ProjectIDIn(projects...))),
		c.Route.Delete().Where(route.OrgID(org), only(scoped, route.ProjectIDIn(projects...))),
		c.RoutingRule.Delete().Where(routingrule.OrgID(org), only(scoped, routingrule.ProjectIDIn(projects...))),
	} {
		if _, err := d.Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// only is p when on, else a predicate that keeps every row.
func only[P ~func(*entsql.Selector)](on bool, p P) P {
	if on {
		return p
	}
	return func(*entsql.Selector) {}
}

// keys matches the Valkey keys of deleted data: those naming the
// organization (when it went) or one of its projects, and the
// organization's rate limit counters, which name it by digest. A row
// cache's change counter waits for the final run (kvcache.IsChangeCounter).
type keys struct {
	ids    map[string]bool
	digest string
	final  bool
}

func keysOf(a Args) keys {
	k := keys{ids: map[string]bool{}, final: a.Final}
	if a.Whole {
		k.ids[a.Org.String()] = true
		k.digest = ratelimit.Digest(platform.OrgKey(a.Org))
	}
	for _, id := range a.Projects {
		k.ids[id.String()] = true
	}
	return k
}

func (k keys) match(key string) bool {
	if !k.final && kvcache.IsChangeCounter(key) {
		return false
	}
	// A UUID is 36 characters with dashes at 8, 13, 18 and 23.
	for i := 0; i+36 <= len(key); i++ {
		if key[i+8] == '-' && key[i+13] == '-' && key[i+18] == '-' && key[i+23] == '-' && k.ids[key[i:i+36]] {
			return true
		}
	}
	i := strings.LastIndexByte(key, ':')
	return k.digest != "" && i >= 0 && key[i+1:] == k.digest
}

type worker struct {
	river.WorkerDefaults[Args]
	s *Service
}

// Work runs the purge; while ClickHouse is still deleting, the job snoozes
// and looks again, which River doesn't count as an attempt.
func (w *worker) Work(ctx context.Context, job *river.Job[Args]) error {
	err := w.s.Purge(ctx, job.Args)
	if errors.Is(err, events.ErrErasing) {
		return river.JobSnooze(eraseWait / 2)
	}
	return err
}

// Timeout gives a run time for large organizations.
func (w *worker) Timeout(*river.Job[Args]) time.Duration { return 30 * time.Minute }

// NextRetry retries a failing purge sooner than River would after a few
// failures, and at least hourly.
func (w *worker) NextRetry(job *river.Job[Args]) time.Time {
	return time.Now().Add(min(time.Duration(job.Attempt*job.Attempt)*time.Minute, time.Hour))
}

// RegisterWorkers registers the purge job. Every binary can enqueue it;
// the worker runs it.
func RegisterWorkers(j *jobs.Jobs, s *Service) {
	jobs.Register(j, &worker{s: s})
}
