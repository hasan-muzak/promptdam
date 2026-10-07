// Package routers keeps a project's model routers (project.md §6.2): tiers
// of models the project describes, rules, a default tier and how calls are
// evaluated. The gateway holds the routers that aren't disabled in the
// project's snapshot and places each call with tiers.Spec.Choose.
package routers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/policy"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/modelrouter"
	"github.com/hasan-muzak/promptdam/server/ent/route"
	"github.com/hasan-muzak/promptdam/server/ent/routerclassifier"
	"github.com/hasan-muzak/promptdam/server/features/classifier"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

// MaxPerProject bounds a project's routers: the gateway holds them all.
const MaxPerProject = 20

// Defaults.
const (
	DefaultQualityFloor = 0.95
	DefaultBudgetMicros = 100_000_000 // $100 a month
	DefaultTimeoutMS    = 150
	// ShadowSampleRate and ActiveSampleRate are the shares of calls
	// evaluated when a router sets none.
	ShadowSampleRate = 0.10
	ActiveSampleRate = 0.02
)

// Errors.
var (
	ErrNotFound  = errors.New("routers: not found")
	ErrNameTaken = errors.New("routers: the name is taken")
	ErrLimit     = errors.New("routers: too many routers")
)

// InvalidError is input that can't be saved; its message says why.
type InvalidError struct {
	Reason string
	// Problems locate a rule's condition errors; Rule is its index.
	Problems []policy.Problem
	Rule     int
}

func (e *InvalidError) Error() string { return "routers: " + e.Reason }

// InUseError refuses deleting a router routes send calls through.
type InUseError struct{ Routes []string }

func (e *InUseError) Error() string {
	return "routers: routes send calls through it: " + strings.Join(e.Routes, ", ")
}

// Input is a router's fields.
type Input struct {
	Name   string
	Status string
	Spec   tiers.Spec
	// QualityFloor is 0.5 to 1 (0: DefaultQualityFloor).
	QualityFloor float64
	// SampleRate is 0 (the mode's default) to 0.5.
	SampleRate float64
	// EvalBudgetMicros caps evaluation a month; 0 turns it off.
	EvalBudgetMicros int64
	Judge            string
	DecisionModel    string
	// DecisionTimeoutMS is 20 to 2,000 (0: DefaultTimeoutMS).
	DecisionTimeoutMS int
}

// Router is a router as the gateway holds it.
type Router struct {
	ID                uuid.UUID  `json:"id"`
	Name              string     `json:"name"`
	Status            string     `json:"status"`
	Spec              tiers.Spec `json:"spec"`
	QualityFloor      float64    `json:"quality_floor"`
	SampleRate        float64    `json:"sample_rate"`
	EvalBudgetMicros  int64      `json:"eval_budget_micros"`
	Judge             string     `json:"judge,omitempty"`
	DecisionModel     string     `json:"decision_model,omitempty"`
	DecisionTimeoutMS int        `json:"decision_timeout_ms"`
	// Classifier is the router's trained classifier, nil before the first
	// training or when it was trained against another default tier.
	Classifier *classifier.Model `json:"classifier,omitempty"`
}

// Rate is the share of calls the router evaluates.
func (r *Router) Rate() float64 {
	switch {
	case r.SampleRate > 0:
		return r.SampleRate
	case r.Status == tiers.Shadow:
		return ShadowSampleRate
	}
	return ActiveSampleRate
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Service keeps routers.
type Service struct {
	ent     *pdent.Client
	env     *policy.Env
	prices  prices.Prices
	changed func(ctx context.Context, project uuid.UUID)
	now     func() time.Time
}

// New returns a Service. env compiles rules (rules.NewEnv); prices keeps
// routers from taking a model's name; changed runs after a project's
// routers change.
func New(ent *pdent.Client, env *policy.Env, p prices.Prices, changed func(ctx context.Context, project uuid.UUID)) *Service {
	if changed == nil {
		changed = func(context.Context, uuid.UUID) {}
	}
	return &Service{ent: ent, env: env, prices: p, changed: changed, now: time.Now}
}

// Check validates a router, its rules compiled.
func (s *Service) Check(in *Input) error {
	in.Name, in.Judge, in.DecisionModel = strings.TrimSpace(in.Name), strings.TrimSpace(in.Judge), strings.TrimSpace(in.DecisionModel)
	if in.QualityFloor == 0 {
		in.QualityFloor = DefaultQualityFloor
	}
	if in.DecisionTimeoutMS == 0 {
		in.DecisionTimeoutMS = DefaultTimeoutMS
	}
	bad := func(format string, args ...any) error {
		return &InvalidError{Reason: fmt.Sprintf(format, args...), Rule: -1}
	}
	switch {
	case !nameRE.MatchString(in.Name):
		return bad("a router's name is 1 to 64 lowercase letters, digits, dots, dashes or underscores")
	case s.isModel(in.Name):
		return bad("%q is a model's name: name the router something clients can't mean as a model", in.Name)
	case in.Status != tiers.Shadow && in.Status != tiers.Active && in.Status != tiers.Disabled:
		return bad("the status is shadow, active or disabled")
	case in.QualityFloor < 0.5 || in.QualityFloor > 1:
		return bad("the quality floor is 0.5 to 1")
	case in.SampleRate < 0 || in.SampleRate > 0.5:
		return bad("the sample rate is 0 (the mode's default) to 0.5")
	case in.EvalBudgetMicros < 0 || in.EvalBudgetMicros > 1_000_000_000_000:
		return bad("the evaluation budget is 0 (off) to $1,000,000 a month")
	case len(in.Judge) > 300 || len(in.DecisionModel) > 300:
		return bad("judge and decision_model are a model, or provider/model")
	case in.DecisionTimeoutMS < 20 || in.DecisionTimeoutMS > 2000:
		return bad("the decision timeout is 20 to 2,000 ms")
	}
	if err := in.Spec.Check(); err != nil {
		var ie *tiers.InvalidError
		if errors.As(err, &ie) {
			return bad("%s", ie.Reason)
		}
		return err
	}
	for i, r := range in.Spec.Rules {
		if _, err := s.env.Compile(r.When); err != nil {
			var ce *policy.CompileError
			if errors.As(err, &ce) {
				return &InvalidError{Reason: fmt.Sprintf("rule %d doesn't compile: %s", i+1, ce.Error()), Problems: ce.Problems, Rule: i}
			}
			return err
		}
	}
	return nil
}

// isModel reports a name the price list knows as a model's.
func (s *Service) isModel(name string) bool {
	if s.prices == nil {
		return false
	}
	m, ok := s.prices.Current().Lookup("", name)
	return ok && m.ID == name
}

// List returns a project's routers by name.
func (s *Service) List(ctx context.Context, project uuid.UUID) ([]*pdent.ModelRouter, error) {
	return s.ent.ModelRouter.Query().Where(modelrouter.ProjectID(project)).Order(pdent.Asc(modelrouter.FieldName)).All(ctx)
}

// Get returns one of a project's routers.
func (s *Service) Get(ctx context.Context, project, id uuid.UUID) (*pdent.ModelRouter, error) {
	r, err := s.ent.ModelRouter.Query().Where(modelrouter.ID(id), modelrouter.ProjectID(project)).Only(ctx)
	if pdent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return r, err
}

// Classifier returns a router's trained classifier (nil before the first
// training).
func (s *Service) Classifier(ctx context.Context, router uuid.UUID) (*pdent.RouterClassifier, error) {
	c, err := s.ent.RouterClassifier.Query().Where(routerclassifier.RouterID(router)).Only(ctx)
	if pdent.IsNotFound(err) {
		return nil, nil
	}
	return c, err
}

// Exists reports whether the project has a router by that name.
func (s *Service) Exists(ctx context.Context, project uuid.UUID, name string) (bool, error) {
	return s.ent.ModelRouter.Query().Where(modelrouter.ProjectID(project), modelrouter.Name(name)).Exist(ctx)
}

// Create adds a router.
func (s *Service) Create(ctx context.Context, org, project uuid.UUID, in Input) (*pdent.ModelRouter, error) {
	if err := s.Check(&in); err != nil {
		return nil, err
	}
	n, err := s.ent.ModelRouter.Query().Where(modelrouter.ProjectID(project)).Count(ctx)
	if err != nil {
		return nil, err
	}
	if n >= MaxPerProject {
		return nil, ErrLimit
	}
	c := s.ent.ModelRouter.Create().SetOrgID(org).SetProjectID(project).SetName(in.Name).SetStatus(modelrouter.Status(in.Status)).
		SetTiers(in.Spec.Tiers).SetRules(rulesOf(in.Spec)).SetDefaultTier(in.Spec.Default).SetQualityFloor(in.QualityFloor).
		SetSampleRate(in.SampleRate).SetEvalBudgetMicros(in.EvalBudgetMicros).SetJudge(in.Judge).
		SetDecisionModel(in.DecisionModel).SetDecisionTimeoutMs(in.DecisionTimeoutMS)
	if in.Status == tiers.Active {
		c.SetActivatedAt(s.now().UTC())
	}
	r, err := c.Save(ctx)
	if pdent.IsConstraintError(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, err
	}
	s.changed(ctx, project)
	return r, nil
}

// Update replaces a router's fields.
func (s *Service) Update(ctx context.Context, project, id uuid.UUID, in Input) (*pdent.ModelRouter, error) {
	if err := s.Check(&in); err != nil {
		return nil, err
	}
	cur, err := s.Get(ctx, project, id)
	if err != nil {
		return nil, err
	}
	if cur.Name != in.Name {
		if routes, err := s.routesThrough(ctx, project, cur.Name); err != nil {
			return nil, err
		} else if len(routes) > 0 {
			return nil, &InUseError{Routes: routes}
		}
	}
	u := s.ent.ModelRouter.UpdateOne(cur).SetName(in.Name).SetStatus(modelrouter.Status(in.Status)).SetTiers(in.Spec.Tiers).
		SetRules(rulesOf(in.Spec)).SetDefaultTier(in.Spec.Default).SetQualityFloor(in.QualityFloor).SetSampleRate(in.SampleRate).
		SetEvalBudgetMicros(in.EvalBudgetMicros).SetJudge(in.Judge).SetDecisionModel(in.DecisionModel).
		SetDecisionTimeoutMs(in.DecisionTimeoutMS)
	if in.Status == tiers.Active && cur.Status != modelrouter.StatusActive {
		u.SetActivatedAt(s.now().UTC())
	}
	r, err := u.Save(ctx)
	if pdent.IsConstraintError(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, err
	}
	s.changed(ctx, project)
	return r, nil
}

// Delete removes a router no route sends calls through.
func (s *Service) Delete(ctx context.Context, project, id uuid.UUID) error {
	cur, err := s.Get(ctx, project, id)
	if err != nil {
		return err
	}
	routes, err := s.routesThrough(ctx, project, cur.Name)
	if err != nil {
		return err
	}
	if len(routes) > 0 {
		return &InUseError{Routes: routes}
	}
	if err := s.ent.ModelRouter.DeleteOne(cur).Exec(ctx); err != nil {
		return err
	}
	s.changed(ctx, project)
	return nil
}

// routesThrough are the names of the project's routes that send their
// calls through the router name.
func (s *Service) routesThrough(ctx context.Context, project uuid.UUID, name string) ([]string, error) {
	return s.ent.Route.Query().Where(route.ProjectID(project), route.Router(name)).Order(pdent.Asc(route.FieldName)).
		Select(route.FieldName).Strings(ctx)
}

// Serving returns a project's routers that aren't disabled, by name, for
// the gateway's snapshot.
func (s *Service) Serving(ctx context.Context, project uuid.UUID) ([]Router, error) {
	rows, err := s.ent.ModelRouter.Query().Where(modelrouter.ProjectID(project), modelrouter.StatusNEQ(modelrouter.StatusDisabled)).
		Order(pdent.Asc(modelrouter.FieldName)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("routers: %w", err)
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	trained, err := s.ent.RouterClassifier.Query().Where(routerclassifier.RouterIDIn(ids...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("routers: classifiers: %w", err)
	}
	models := make(map[uuid.UUID]*classifier.Model, len(trained))
	for _, c := range trained {
		models[c.RouterID] = &c.Model
	}
	out := make([]Router, len(rows))
	for i, r := range rows {
		out[i] = Of(r)
		if m := models[r.ID]; m != nil && m.Default == r.DefaultTier {
			out[i].Classifier = m
		}
	}
	return out, nil
}

// Of is a stored router as the gateway holds it.
func Of(r *pdent.ModelRouter) Router {
	return Router{
		ID: r.ID, Name: r.Name, Status: string(r.Status),
		Spec:         tiers.Spec{Tiers: r.Tiers, Rules: r.Rules, Default: r.DefaultTier},
		QualityFloor: r.QualityFloor, SampleRate: r.SampleRate, EvalBudgetMicros: r.EvalBudgetMicros, Judge: r.Judge,
		DecisionModel: r.DecisionModel, DecisionTimeoutMS: r.DecisionTimeoutMs,
	}
}

func rulesOf(s tiers.Spec) []tiers.Rule {
	if s.Rules == nil {
		return []tiers.Rule{}
	}
	return s.Rules
}
