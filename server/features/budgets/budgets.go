// Package budgets caps what projects spend on LLM calls: per project, API
// key, developer or route, per day or month (UTC). Crossing a threshold
// alerts the notification channels and sends a webhook once per window; a
// blocking budget also makes the gateway refuse the scope's calls until the
// window ends.
package budgets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/money"
	"github.com/hasan-muzak/keel-core/pkg/notify"
	"github.com/hasan-muzak/keel-core/pkg/webhooks"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/budget"
)

// IDPrefix starts budget IDs: bdg_….
const IDPrefix = "bdg"

// MaxPerProject bounds a project's budgets: the gateway holds the blocking
// ones and a job checks them all every minute.
const MaxPerProject = 100

// Webhook events.
var (
	EventThreshold = webhooks.EventType{Name: "budget.threshold_reached",
		Description: "A scope's LLM spend crossed a share of a budget for its day or month."}
	EventExceeded = webhooks.EventType{Name: "budget.exceeded",
		Description: "A scope spent its whole budget for its day or month; a blocking budget now refuses its calls."}
)

// Errors.
var (
	ErrNotFound = errors.New("budgets: not found")
	ErrLimit    = errors.New("budgets: too many budgets")
)

// InvalidError is input that can't be saved; its message says why.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "budgets: " + e.Reason }

// Config configures a Service.
type Config struct {
	Ent      *pdent.Client
	Spend    *Spend
	Notifier *notify.Notifier
	Webhooks *webhooks.Service
	AppURL   string
	// Changed runs after a project's budgets change (the gateway holds the
	// blocking ones).
	Changed func(ctx context.Context, project uuid.UUID)
	Now     func() time.Time
}

// Service keeps budgets and checks them.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.Changed == nil {
		cfg.Changed = func(context.Context, uuid.UUID) {}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}
}

// Input is a budget's fields.
type Input struct {
	Name        string
	Scope       string
	Target      string
	Window      string
	LimitMicros int64
	Action      string
	Thresholds  []int
	Channels    []uuid.UUID
	Enabled     bool
}

func check(in *Input) error {
	bad := func(format string, args ...any) error { return &InvalidError{Reason: fmt.Sprintf(format, args...)} }
	in.Name, in.Target = strings.TrimSpace(in.Name), strings.TrimSpace(in.Target)
	switch {
	case in.Name == "" || len(in.Name) > 100:
		return bad("a name is 1 to 100 characters")
	case !slices.Contains([]string{ScopeProject, ScopeKey, ScopeDeveloper, ScopeRoute}, in.Scope):
		return bad("the scope is project, key, developer or route")
	case in.Scope == ScopeProject && in.Target != "":
		return bad("a project budget has no target")
	case in.Scope != ScopeProject && in.Target == "":
		return bad("a %s budget names its %s", in.Scope, in.Scope)
	case len(in.Target) > 200:
		return bad("the target is at most 200 characters")
	case in.Window != "day" && in.Window != "month":
		return bad("the window is day or month")
	case in.LimitMicros < 1 || in.LimitMicros > 1_000_000_000_000:
		return bad("the limit is 1 to 1,000,000,000,000 micro-USD")
	case in.Action != "alert" && in.Action != "block":
		return bad("the action is alert or block")
	case len(in.Thresholds) > 5:
		return bad("at most 5 thresholds")
	}
	if len(in.Thresholds) == 0 {
		in.Thresholds = []int{80, 100}
	}
	slices.Sort(in.Thresholds)
	in.Thresholds = slices.Compact(in.Thresholds)
	for _, t := range in.Thresholds {
		if t < 1 || t > 1000 {
			return bad("a threshold is 1 to 1000 percent")
		}
	}
	return nil
}

func channelIDs(cs []uuid.UUID) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.String()
	}
	return out
}

// List returns a project's budgets.
func (s *Service) List(ctx context.Context, project uuid.UUID) ([]*pdent.Budget, error) {
	return s.cfg.Ent.Budget.Query().Where(budget.ProjectID(project)).Order(pdent.Asc(budget.FieldName)).All(ctx)
}

// Get returns one of a project's budgets.
func (s *Service) Get(ctx context.Context, project, id uuid.UUID) (*pdent.Budget, error) {
	b, err := s.cfg.Ent.Budget.Query().Where(budget.ID(id), budget.ProjectID(project)).Only(ctx)
	if pdent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return b, err
}

// Create adds a budget.
func (s *Service) Create(ctx context.Context, org, project uuid.UUID, in Input) (*pdent.Budget, error) {
	if err := check(&in); err != nil {
		return nil, err
	}
	n, err := s.cfg.Ent.Budget.Query().Where(budget.ProjectID(project)).Count(ctx)
	if err != nil {
		return nil, err
	}
	if n >= MaxPerProject {
		return nil, ErrLimit
	}
	b, err := s.cfg.Ent.Budget.Create().SetOrgID(org).SetProjectID(project).
		SetName(in.Name).SetScope(budget.Scope(in.Scope)).SetTarget(in.Target).SetWindow(budget.Window(in.Window)).
		SetLimitMicros(in.LimitMicros).SetAction(budget.Action(in.Action)).SetThresholds(in.Thresholds).
		SetChannelIds(channelIDs(in.Channels)).SetEnabled(in.Enabled).Save(ctx)
	if err != nil {
		return nil, err
	}
	s.cfg.Changed(ctx, project)
	return b, nil
}

// Update replaces a budget's fields. A new scope, window or limit starts
// its alerts again.
func (s *Service) Update(ctx context.Context, project, id uuid.UUID, in Input) (*pdent.Budget, error) {
	if err := check(&in); err != nil {
		return nil, err
	}
	cur, err := s.Get(ctx, project, id)
	if err != nil {
		return nil, err
	}
	u := s.cfg.Ent.Budget.UpdateOne(cur).
		SetName(in.Name).SetScope(budget.Scope(in.Scope)).SetTarget(in.Target).SetWindow(budget.Window(in.Window)).
		SetLimitMicros(in.LimitMicros).SetAction(budget.Action(in.Action)).SetThresholds(in.Thresholds).
		SetChannelIds(channelIDs(in.Channels)).SetEnabled(in.Enabled)
	if string(cur.Scope) != in.Scope || cur.Target != in.Target || string(cur.Window) != in.Window || cur.LimitMicros != in.LimitMicros {
		u.ClearAlertedPeriod().SetAlertedPercent(0)
	}
	b, err := u.Save(ctx)
	if err != nil {
		return nil, err
	}
	s.cfg.Changed(ctx, project)
	return b, nil
}

// Delete removes a budget.
func (s *Service) Delete(ctx context.Context, project, id uuid.UUID) error {
	n, err := s.cfg.Ent.Budget.Delete().Where(budget.ID(id), budget.ProjectID(project)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.cfg.Changed(ctx, project)
	return nil
}

// Status is a budget's current window.
type Status struct {
	WindowStart, WindowEnd time.Time
	SpentMicros            int64
	// Percent of the limit spent (100: all of it).
	Percent float64
}

// Status reads a budget's spend in its current window.
func (s *Service) Status(ctx context.Context, b *pdent.Budget) (Status, error) {
	now := s.cfg.Now()
	w := windowOf(string(b.Window))
	spent, err := s.cfg.Spend.Total(ctx, b.ProjectID, string(b.Scope), b.Target, w, now)
	if err != nil {
		return Status{}, err
	}
	start := w.Start(now)
	return Status{
		WindowStart: start, WindowEnd: w.End(start), SpentMicros: spent,
		Percent: float64(spent) * 100 / float64(b.LimitMicros),
	}, nil
}

// Blocking is a blocking budget as the gateway holds it.
type Blocking struct {
	ID          uuid.UUID
	Scope       string
	Target      string
	Window      string
	LimitMicros int64
}

// Blockings returns a project's enabled blocking budgets.
func (s *Service) Blockings(ctx context.Context, project uuid.UUID) ([]Blocking, error) {
	bs, err := s.cfg.Ent.Budget.Query().
		Where(budget.ProjectID(project), budget.Enabled(true), budget.ActionEQ(budget.ActionBlock)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Blocking, len(bs))
	for i, b := range bs {
		out[i] = Blocking{ID: b.ID, Scope: string(b.Scope), Target: b.Target, Window: string(b.Window), LimitMicros: b.LimitMicros}
	}
	return out, nil
}

// Exceeded returns the first blocking budget that who has spent, and when
// its window ends. It reads the gateway's counters: no round trip once a
// scope is known.
func (s *Spend) Exceeded(ctx context.Context, who Who, bs []Blocking) (*Blocking, time.Time, error) {
	for i, b := range bs {
		if !who.covers(b.Scope, b.Target) {
			continue
		}
		w := windowOf(b.Window)
		spent, err := s.Spent(ctx, who.Project, b.Scope, b.Target, w)
		if err != nil {
			return nil, time.Time{}, err
		}
		if spent >= b.LimitMicros {
			return &bs[i], w.End(w.Start(time.Now())), nil
		}
	}
	return nil, time.Time{}, nil
}

func (w Who) covers(scope, target string) bool {
	switch scope {
	case ScopeProject:
		return true
	case ScopeKey:
		return w.Key != uuid.Nil && w.Key.String() == target
	case ScopeDeveloper:
		return w.Developer != "" && w.Developer == target
	case ScopeRoute:
		return w.Route != "" && w.Route == target
	}
	return false
}

// CheckArgs is the job checking every budget.
type CheckArgs struct{}

// Kind implements river.JobArgs.
func (CheckArgs) Kind() string { return "promptdam_budgets_check" }

// Check alerts on every budget that crossed a threshold in its current
// window, once per threshold and window.
func (s *Service) Check(ctx context.Context) error {
	all, err := s.cfg.Ent.Budget.Query().Where(budget.Enabled(true)).All(entx.SkipTenant(ctx))
	if err != nil {
		return err
	}
	var failed error
	for _, b := range all {
		ctx := entx.WithOrg(ctx, b.OrgID)
		st, err := s.Status(ctx, b)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		if err := s.check(ctx, b, st); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

func (s *Service) check(ctx context.Context, b *pdent.Budget, st Status) error {
	crossed := 0
	for _, t := range b.Thresholds {
		if st.Percent >= float64(t) {
			crossed = t
		}
	}
	if crossed == 0 {
		return nil
	}
	// Claim the alert, so concurrent checks send it once.
	n, err := s.cfg.Ent.Budget.Update().
		Where(budget.ID(b.ID), budget.Or(budget.AlertedPeriodIsNil(), budget.AlertedPeriodNEQ(st.WindowStart),
			budget.AlertedPercentLT(crossed))).
		SetAlertedPeriod(st.WindowStart).SetAlertedPercent(crossed).
		Save(ctx)
	if err != nil || n == 0 {
		return err
	}
	if err := s.alert(ctx, b, st, crossed); err != nil {
		// Give the claim back: the next check tries again.
		u := s.cfg.Ent.Budget.UpdateOneID(b.ID).SetAlertedPercent(b.AlertedPercent)
		if b.AlertedPeriod != nil {
			u.SetAlertedPeriod(*b.AlertedPeriod)
		} else {
			u.ClearAlertedPeriod()
		}
		return errors.Join(err, u.Exec(ctx))
	}
	return nil
}

// Payload is a budget in webhooks.
type Payload struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	Name        string    `json:"name"`
	Scope       string    `json:"scope"`
	Target      string    `json:"target"`
	Window      string    `json:"window"`
	WindowStart time.Time `json:"window_start"`
	LimitMicros int64     `json:"limit_micros"`
	SpentMicros int64     `json:"spent_micros"`
	Threshold   int       `json:"threshold"`
	Blocking    bool      `json:"blocking"`
	URL         string    `json:"url"`
}

func (s *Service) alert(ctx context.Context, b *pdent.Budget, st Status, crossed int) error {
	id := ids.Encode(IDPrefix, b.ID)
	url := strings.TrimRight(s.cfg.AppURL, "/") + "/budgets/" + id
	scope := "The project"
	if b.Scope != budget.ScopeProject {
		scope = fmt.Sprintf("The %s %s", b.Scope, b.Target)
	}
	severity, event := "warning", EventThreshold
	note := ""
	if crossed >= 100 {
		severity, event = "critical", EventExceeded
		if b.Action == budget.ActionBlock {
			note = "; its calls are refused until the window ends"
		}
	}
	var channels []uuid.UUID
	for _, c := range b.ChannelIds {
		if id, err := uuid.Parse(c); err == nil {
			channels = append(channels, id)
		}
	}
	err := s.cfg.Notifier.AlertTo(ctx, b.OrgID, channels, notify.Alert{
		Title:    fmt.Sprintf("%s reached %d%% of its %s LLM budget", scope, crossed, b.Window),
		Body:     fmt.Sprintf("%s of %s spent%s · budget %q", money.Micros(st.SpentMicros), money.Micros(b.LimitMicros), note, b.Name),
		URL:      url,
		Severity: severity,
	})
	if err != nil {
		return fmt.Errorf("budgets: alerting: %w", err)
	}
	if s.cfg.Webhooks != nil {
		p := Payload{
			ID: id, Project: ids.Encode(ids.Project, b.ProjectID), Name: b.Name, Scope: string(b.Scope), Target: b.Target,
			Window: string(b.Window), WindowStart: st.WindowStart, LimitMicros: b.LimitMicros, SpentMicros: st.SpentMicros,
			Threshold: crossed, Blocking: b.Action == budget.ActionBlock, URL: url,
		}
		if err := s.cfg.Webhooks.Publish(ctx, b.OrgID, event.Name, map[string]any{"budget": p}); err != nil {
			slog.Warn("budgets: publishing a webhook", "budget", b.ID, "err", err)
		}
	}
	return nil
}

type checkWorker struct {
	river.WorkerDefaults[CheckArgs]
	s *Service
}

func (w *checkWorker) Work(ctx context.Context, _ *river.Job[CheckArgs]) error { return w.s.Check(ctx) }

// RegisterWorkers checks every budget each minute.
func RegisterWorkers(j *jobs.Jobs, s *Service) {
	jobs.Register(j, &checkWorker{s: s})
	j.Every("promptdam-budgets-check", time.Minute, func() river.JobArgs { return CheckArgs{} })
}
