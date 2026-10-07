package handlers

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/policy"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/rules"
)

// RulePrefix starts routing rule IDs: rul_….
const RulePrefix = "rul"

// Error codes of the rule routes.
const (
	CodeInvalidRule   = "invalid_rule"
	CodeRuleNameTaken = "rule_name_taken"
	CodeRuleLimit     = "rule_limit"
)

// RuleDeps are what the rule routes need beyond the rules service.
type RuleDeps struct {
	Env    *policy.Env
	Events *events.DB
	Prices prices.Prices
}

// RuleOut is a routing rule.
type RuleOut struct {
	ID          string     `json:"id" doc:"Rule ID (rul_…)"`
	Name        string     `json:"name"`
	Route       string     `json:"route" doc:"The route whose calls it reads; empty for every route"`
	When        string     `json:"when" doc:"CEL condition over request (model, format, input_tokens, messages, max_tokens, has_tools, json, stream, family, tags, user, developer, route)"`
	ToModel     string     `json:"to_model" doc:"The model matching calls go to: model, or provider/model"`
	Priority    int        `json:"priority" doc:"Lower first; the first active rule that holds routes the call"`
	Status      string     `json:"status" doc:"draft (tried with dry-runs only), active or disabled"`
	ActivatedAt *time.Time `json:"activated_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func ruleOut(r *pdent.RoutingRule) RuleOut {
	return RuleOut{ID: ids.Encode(RulePrefix, r.ID), Name: r.Name, Route: r.Route, When: r.When, ToModel: r.ToModel,
		Priority: r.Priority, Status: string(r.Status), ActivatedAt: r.ActivatedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// RuleIn is a rule's fields.
type RuleIn struct {
	Name     string `json:"name"`
	Route    string `json:"route,omitempty"`
	When     string `json:"when" doc:"e.g. request.input_tokens < 1500 && !request.has_tools"`
	ToModel  string `json:"to_model"`
	Priority int    `json:"priority,omitempty"`
	Status   string `json:"status,omitempty" doc:"draft (default), active or disabled"`
}

func (in *RuleIn) validate(v *muzak.Validation) {
	v.String(&in.Name).Trim().Required().MaxLen(100)
	v.String(&in.When).Required().MaxLen(4096)
	v.String(&in.ToModel).Trim().Required().MaxLen(300)
}

func (in RuleIn) input() rules.Input {
	status := in.Status
	if status == "" {
		status = rules.Draft
	}
	return rules.Input{Name: in.Name, Route: in.Route, When: in.When, ToModel: in.ToModel, Status: status, Priority: in.Priority}
}

// CreateRuleIn adds a rule.
type CreateRuleIn struct {
	platform.ProjectPath
	RuleIn
}

// Validate implements muzak.Validatable.
func (in *CreateRuleIn) Validate(v *muzak.Validation) { in.validate(v) }

// RulePath names a rule.
type RulePath struct {
	platform.ProjectPath
	Rule string `path:"rule" doc:"Rule ID (rul_…)"`
}

// UpdateRuleIn replaces a rule.
type UpdateRuleIn struct {
	RulePath
	RuleIn
}

// Validate implements muzak.Validatable.
func (in *UpdateRuleIn) Validate(v *muzak.Validation) { in.validate(v) }

// ListRules lists a project's routing rules in evaluation order.
func (h *Handlers) ListRules(ctx *muzak.Context, _ platform.ProjectPath) (httpx.List[RuleOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	rs, err := h.Rules.List(ctx.Context(), pa.Project.ID)
	if err != nil {
		return httpx.List[RuleOut]{}, err
	}
	out := httpx.List[RuleOut]{Data: make([]RuleOut, len(rs))}
	for i, r := range rs {
		out.Data[i] = ruleOut(r)
	}
	return out, nil
}

// CreateRule adds a routing rule.
func (h *Handlers) CreateRule(ctx *muzak.Context, in CreateRuleIn) (RuleOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	r, err := h.Rules.Create(ctx.Context(), pa.OrgID(), pa.Project.ID, in.input())
	if err != nil {
		return RuleOut{}, ruleFail(err)
	}
	return ruleOut(r), nil
}

// GetRule returns a rule.
func (h *Handlers) GetRule(ctx *muzak.Context, in RulePath) (RuleOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := ruleID(in.Rule)
	if err != nil {
		return RuleOut{}, err
	}
	r, err := h.Rules.Get(ctx.Context(), pa.Project.ID, id)
	if err != nil {
		return RuleOut{}, ruleFail(err)
	}
	return ruleOut(r), nil
}

// UpdateRule replaces a rule.
func (h *Handlers) UpdateRule(ctx *muzak.Context, in UpdateRuleIn) (RuleOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := ruleID(in.Rule)
	if err != nil {
		return RuleOut{}, err
	}
	r, err := h.Rules.Update(ctx.Context(), pa.Project.ID, id, in.input())
	if err != nil {
		return RuleOut{}, ruleFail(err)
	}
	return ruleOut(r), nil
}

// DeleteRule removes a rule.
func (h *Handlers) DeleteRule(ctx *muzak.Context, in RulePath) (muzak.Empty, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := ruleID(in.Rule)
	if err != nil {
		return muzak.Empty{}, err
	}
	return muzak.Empty{}, ruleFail(h.Rules.Delete(ctx.Context(), pa.Project.ID, id))
}

// DryRunIn tries a rule, saved or not, on recent calls.
type DryRunIn struct {
	platform.ProjectPath
	TimeRange
	Route   string `json:"route,omitempty"`
	When    string `json:"when"`
	ToModel string `json:"to_model"`
}

// Validate implements muzak.Validatable.
func (in *DryRunIn) Validate(v *muzak.Validation) {
	v.String(&in.When).Required().MaxLen(4096)
	v.String(&in.ToModel).Trim().Required().MaxLen(300)
}

// DryRunRuleIn tries a saved rule on recent calls.
type DryRunRuleIn struct {
	RulePath
	TimeRange
}

// DryRunOut is what a rule would have done to recent calls.
type DryRunOut struct {
	From            time.Time `json:"from"`
	To              time.Time `json:"to"`
	Calls           uint64    `json:"calls" doc:"The calls read: the most recent 20,000 of the range at most"`
	Matched         uint64    `json:"matched" doc:"Those the rule would have routed"`
	Unpriced        uint64    `json:"unpriced" doc:"Matched calls whose cost on the rule's model is unknown, left out of the sums"`
	CostMicros      int64     `json:"cost_micros" doc:"What the matched calls cost"`
	EstimatedMicros int64     `json:"estimated_micros" doc:"What they would have cost on the rule's model, with the same tokens"`
	SavingsMicros   int64     `json:"savings_micros"`
	Note            string    `json:"note"`
}

// DryRunRule tries a rule that isn't saved.
func (h *Handlers) DryRunRule(ctx *muzak.Context, in DryRunIn) (DryRunOut, error) {
	return h.dryRun(ctx, rules.Rule{Name: "dry-run", Route: in.Route, When: in.When, ToModel: in.ToModel}, in.TimeRange)
}

// DryRunSavedRule tries a saved rule.
func (h *Handlers) DryRunSavedRule(ctx *muzak.Context, in DryRunRuleIn) (DryRunOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := ruleID(in.Rule)
	if err != nil {
		return DryRunOut{}, err
	}
	r, err := h.Rules.Get(ctx.Context(), pa.Project.ID, id)
	if err != nil {
		return DryRunOut{}, ruleFail(err)
	}
	return h.dryRun(ctx, rules.Rule{Name: r.Name, Route: r.Route, When: r.When, ToModel: r.ToModel}, in.TimeRange)
}

func (h *Handlers) dryRun(ctx *muzak.Context, rule rules.Rule, r TimeRange) (DryRunOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := r.bounds(7 * 24 * time.Hour)
	if err != nil {
		return DryRunOut{}, err
	}
	own, err := h.Prices.All(ctx.Context(), pa.Project.ID)
	if err != nil {
		return DryRunOut{}, err
	}
	d, err := rules.Try(ctx.Context(), h.RuleDeps.Events, h.RuleDeps.Env, h.RuleDeps.Prices, prices.ProjectPrices(own),
		pa.Project.ID, rule, from, to)
	if errors.Is(err, rules.ErrRange) {
		return DryRunOut{}, muzak.UnprocessableEntity("the time range must start before it ends and cover at most 31 days").
			WithCode(CodeInvalidQuery)
	}
	if err != nil {
		return DryRunOut{}, ruleFail(err)
	}
	return DryRunOut{From: from, To: to, Calls: d.Calls, Matched: d.Matched, Unpriced: d.Unpriced, CostMicros: d.CostMicros,
		EstimatedMicros: d.EstimatedMicros, SavingsMicros: d.SavingsMicros,
		Note: "Cost only: the answers' quality on the rule's model isn't known until it is tried on them."}, nil
}

func ruleID(raw string) (uuid.UUID, error) {
	id, err := ids.Decode(RulePrefix, raw)
	if err != nil {
		return id, httpx.NotFound("rule")
	}
	return id, nil
}

func ruleFail(err error) error {
	var bad *rules.InvalidError
	var ce *policy.CompileError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return muzak.UnprocessableEntity(bad.Reason).WithCode(CodeInvalidRule).WithDetails(problems(bad.Problems)...)
	case errors.As(err, &ce):
		return muzak.UnprocessableEntity("the condition doesn't compile").WithCode(CodeInvalidRule).
			WithDetails(problems(ce.Problems)...)
	case errors.Is(err, rules.ErrNotFound):
		return httpx.NotFound("rule")
	case errors.Is(err, rules.ErrNameTaken):
		return muzak.Conflict("another rule of the project has this name").WithCode(CodeRuleNameTaken)
	case errors.Is(err, rules.ErrLimit):
		return muzak.Conflict("a project has at most 50 rules").WithCode(CodeRuleLimit)
	}
	return err
}

// problems locate a condition's errors for an editor.
func problems(ps []policy.Problem) []muzak.ErrorDetail {
	out := make([]muzak.ErrorDetail, len(ps))
	for i, p := range ps {
		out[i] = muzak.ErrorDetail{Field: "when", Location: "body",
			Issue: fmt.Sprintf("line %d, column %d: %s", p.Line, p.Column, p.Message)}
	}
	return out
}
