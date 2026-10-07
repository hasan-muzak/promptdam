// Package rules keeps a project's routing rules and decides, for each
// call, which rule (if any) sends it to another model. A rule is a CEL
// condition over the request as the gateway sees it, before any provider
// counted it:
//
//	request.model        string  the model asked for
//	request.format       string  openai.chat, openai.responses, anthropic.messages…
//	request.input_tokens int     estimated: the body's bytes / 4
//	request.messages     int     messages (input items)
//	request.max_tokens   int     the answer's limit, 0 when unset
//	request.has_tools    bool
//	request.json         bool    a JSON answer format was asked for
//	request.stream       bool
//	request.family       string  the prompt family
//	request.tags         list    the key's and the call's tags
//	request.user, request.developer, request.route  string
//
// e.g. `request.input_tokens < 1500 && !request.has_tools`. Rules start as
// drafts, which a dry-run tries on the request log; active rules route.
package rules

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"
	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/policy"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/routingrule"
)

// MaxPerProject bounds a project's rules: the gateway evaluates the
// active ones on every call.
const MaxPerProject = 50

// Statuses.
const (
	Draft    = "draft"
	Active   = "active"
	Disabled = "disabled"
)

// Errors.
var (
	ErrNotFound  = errors.New("rules: not found")
	ErrNameTaken = errors.New("rules: the name is taken")
	ErrLimit     = errors.New("rules: too many rules")
)

// InvalidError is input that can't be saved; its message says why.
type InvalidError struct {
	Reason string
	// Problems locate a condition's errors.
	Problems []policy.Problem
}

func (e *InvalidError) Error() string { return "rules: " + e.Reason }

// NewEnv is the environment rules are compiled in: request is typed, so
// a misspelt field or a comparison of the wrong types is refused when the
// rule is saved, not skipped on every call.
func NewEnv() (*policy.Env, error) {
	return policy.NewEnv(ext.NativeTypes(reflect.TypeFor[Request](), ext.ParseStructTags(true)),
		cel.Variable("request", cel.ObjectType(reflect.TypeFor[Request]().String())))
}

// Request is a call as rules see it.
type Request struct {
	Model       string   `cel:"model"`
	Format      string   `cel:"format"`
	Family      string   `cel:"family"`
	User        string   `cel:"user"`
	Developer   string   `cel:"developer"`
	Route       string   `cel:"route"`
	InputTokens int      `cel:"input_tokens"`
	Messages    int      `cel:"messages"`
	MaxTokens   int      `cel:"max_tokens"`
	HasTools    bool     `cel:"has_tools"`
	JSON        bool     `cel:"json"`
	Stream      bool     `cel:"stream"`
	Tags        []string `cel:"tags"`
}

// Vars are the request's variables.
func (r Request) Vars() map[string]any {
	if r.Tags == nil {
		r.Tags = []string{}
	}
	return map[string]any{"request": r}
}

// Rule is an active rule as the gateway holds it.
type Rule struct {
	Name    string `json:"name"`
	Route   string `json:"route"`
	When    string `json:"when"`
	ToModel string `json:"to_model"`
}

// Match returns the first rule (in order) of route that holds for a call;
// rules failing to evaluate don't hold.
func Match(env *policy.Env, rs []Rule, route string, r Request) (Rule, bool) {
	var vars map[string]any
	for _, rule := range rs {
		if rule.Route != "" && rule.Route != route {
			continue
		}
		p, err := env.Compile(rule.When)
		if err != nil {
			continue
		}
		if vars == nil {
			vars = r.Vars()
		}
		if ok, err := p.Bool(vars); err == nil && ok {
			return rule, true
		}
	}
	return Rule{}, false
}

// Input is a rule's fields.
type Input struct {
	Name, Route, When, ToModel, Status string
	Priority                           int
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._:-]{0,99}$`)

// Service keeps rules.
type Service struct {
	ent     *pdent.Client
	env     *policy.Env
	changed func(ctx context.Context, project uuid.UUID)
	now     func() time.Time
}

// New returns a Service; changed runs after a project's rules change.
func New(ent *pdent.Client, env *policy.Env, changed func(ctx context.Context, project uuid.UUID)) *Service {
	if changed == nil {
		changed = func(context.Context, uuid.UUID) {}
	}
	return &Service{ent: ent, env: env, changed: changed, now: time.Now}
}

// Check validates a rule, its condition compiled.
func (s *Service) Check(in *Input) error {
	in.Name, in.Route, in.ToModel = strings.TrimSpace(in.Name), strings.TrimSpace(in.Route), strings.TrimSpace(in.ToModel)
	switch {
	case !nameRE.MatchString(in.Name):
		return &InvalidError{Reason: "a name is 1 to 100 letters, digits, spaces or ._:-"}
	case len(in.Route) > 100:
		return &InvalidError{Reason: "a route's name is at most 100 characters"}
	case in.ToModel == "" || len(in.ToModel) > 300:
		return &InvalidError{Reason: "to_model is a model, or provider/model"}
	case in.Status != Draft && in.Status != Active && in.Status != Disabled:
		return &InvalidError{Reason: "the status is draft, active or disabled"}
	case in.Priority < -1000 || in.Priority > 1000:
		return &InvalidError{Reason: "the priority is -1000 to 1000"}
	}
	if _, err := s.env.Compile(in.When); err != nil {
		var ce *policy.CompileError
		if errors.As(err, &ce) {
			return &InvalidError{Reason: "the condition doesn't compile: " + ce.Error(), Problems: ce.Problems}
		}
		return err
	}
	return nil
}

// List returns a project's rules in evaluation order.
func (s *Service) List(ctx context.Context, project uuid.UUID) ([]*pdent.RoutingRule, error) {
	return s.ent.RoutingRule.Query().Where(routingrule.ProjectID(project)).
		Order(pdent.Asc(routingrule.FieldPriority), pdent.Asc(routingrule.FieldName)).All(ctx)
}

// Get returns one of a project's rules.
func (s *Service) Get(ctx context.Context, project, id uuid.UUID) (*pdent.RoutingRule, error) {
	r, err := s.ent.RoutingRule.Query().Where(routingrule.ID(id), routingrule.ProjectID(project)).Only(ctx)
	if pdent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return r, err
}

// Create adds a rule.
func (s *Service) Create(ctx context.Context, org, project uuid.UUID, in Input) (*pdent.RoutingRule, error) {
	if err := s.Check(&in); err != nil {
		return nil, err
	}
	n, err := s.ent.RoutingRule.Query().Where(routingrule.ProjectID(project)).Count(ctx)
	if err != nil {
		return nil, err
	}
	if n >= MaxPerProject {
		return nil, ErrLimit
	}
	c := s.ent.RoutingRule.Create().SetOrgID(org).SetProjectID(project).SetName(in.Name).SetRoute(in.Route).
		SetWhen(in.When).SetToModel(in.ToModel).SetPriority(in.Priority).SetStatus(routingrule.Status(in.Status))
	if in.Status == Active {
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

// Update replaces a rule.
func (s *Service) Update(ctx context.Context, project, id uuid.UUID, in Input) (*pdent.RoutingRule, error) {
	if err := s.Check(&in); err != nil {
		return nil, err
	}
	cur, err := s.Get(ctx, project, id)
	if err != nil {
		return nil, err
	}
	u := s.ent.RoutingRule.UpdateOne(cur).SetName(in.Name).SetRoute(in.Route).SetWhen(in.When).SetToModel(in.ToModel).
		SetPriority(in.Priority).SetStatus(routingrule.Status(in.Status))
	if in.Status == Active && cur.Status != routingrule.StatusActive {
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

// Delete removes a rule.
func (s *Service) Delete(ctx context.Context, project, id uuid.UUID) error {
	n, err := s.ent.RoutingRule.Delete().Where(routingrule.ID(id), routingrule.ProjectID(project)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.changed(ctx, project)
	return nil
}

// Actives returns a project's active rules in evaluation order.
func (s *Service) Actives(ctx context.Context, project uuid.UUID) ([]Rule, error) {
	rows, err := s.ent.RoutingRule.Query().Where(routingrule.ProjectID(project), routingrule.StatusEQ(routingrule.StatusActive)).
		Order(pdent.Asc(routingrule.FieldPriority), pdent.Asc(routingrule.FieldName)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	out := make([]Rule, len(rows))
	for i, r := range rows {
		out[i] = Rule{Name: r.Name, Route: r.Route, When: r.When, ToModel: r.ToModel}
	}
	return out, nil
}
