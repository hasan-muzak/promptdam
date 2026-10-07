package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/catalog"
	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

// RouterPrefix starts model router IDs: rtr_….
const RouterPrefix = "rtr"

// Error codes of the router routes.
const (
	CodeInvalidRouter   = "invalid_router"
	CodeRouterNameTaken = "router_name_taken"
	CodeRouterLimit     = "router_limit"
	CodeRouterInUse     = "router_in_use"
)

// RouterDeps are what the router routes need beyond the routers service.
type RouterDeps struct {
	// Explainer places calls as the gateway does.
	Explainer *resolver.Resolver
	// Sources build a project's configuration as gateways hold it.
	Sources snapshot.Sources
	Billing *billing.Service
	AppURL  string
}

// TierOut is a router's tier.
type TierOut struct {
	Name        string   `json:"name" doc:"1 to 32 lowercase letters, digits, - or _"`
	Description string   `json:"description" doc:"What the tier is for, in plain words; the decision model reads it"`
	Model       string   `json:"model" doc:"The tier's model: model, or provider/model, of either API format"`
	Fallbacks   []string `json:"fallbacks" doc:"Tried in order when the model fails, before the next stronger tier"`
	Examples    []string `json:"examples" doc:"Requests the tier is for, as the decision model sees them"`
}

// TierRuleOut sends calls to a tier.
type TierRuleOut struct {
	When string `json:"when" doc:"CEL condition over request (model, format, input_tokens, messages, max_tokens, has_tools, json, stream, family, tags, user, developer, route)"`
	Tier string `json:"tier"`
}

// RouterOut is a model router.
type RouterOut struct {
	ID                string        `json:"id" doc:"Router ID (rtr_…)"`
	Name              string        `json:"name" doc:"Clients call it as a model: name, or name:tier for a tier of their choice"`
	Status            string        `json:"status" doc:"shadow (decides and evaluates, serves the default tier), active or disabled"`
	Tiers             []TierOut     `json:"tiers" doc:"From cheapest to strongest"`
	Rules             []TierRuleOut `json:"rules" doc:"In order: the first that holds places the call"`
	DefaultTier       string        `json:"default_tier" doc:"Calls nothing places get it; the others' answers are judged against it"`
	QualityFloor      float64       `json:"quality_floor" doc:"The share of a tier's answers that must hold up against the default tier's"`
	SampleRate        float64       `json:"sample_rate" doc:"The share of calls evaluated; 0: 10% in shadow mode, 2% active"`
	EvalBudgetMicros  int64         `json:"eval_budget_micros" doc:"What evaluation may spend a month on the project's keys; 0: off"`
	Judge             string        `json:"judge" doc:"The model that judges answers; empty: claude-sonnet-5-5 when the project has an Anthropic provider"`
	DecisionModel     string        `json:"decision_model" doc:"Asked when the classifier is unsure; empty: never"`
	DecisionTimeoutMS int           `json:"decision_timeout_ms"`
	ActivatedAt       *time.Time    `json:"activated_at"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
}

func routerOut(r *pdent.ModelRouter) RouterOut {
	out := RouterOut{ID: ids.Encode(RouterPrefix, r.ID), Name: r.Name, Status: string(r.Status), DefaultTier: r.DefaultTier,
		QualityFloor: r.QualityFloor, SampleRate: r.SampleRate, EvalBudgetMicros: r.EvalBudgetMicros, Judge: r.Judge,
		DecisionModel: r.DecisionModel, DecisionTimeoutMS: r.DecisionTimeoutMs, ActivatedAt: r.ActivatedAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Tiers: make([]TierOut, len(r.Tiers)), Rules: make([]TierRuleOut, len(r.Rules))}
	for i, t := range r.Tiers {
		out.Tiers[i] = TierOut{Name: t.Name, Description: t.Description, Model: t.Model, Fallbacks: orEmpty(t.Fallbacks),
			Examples: orEmpty(t.Examples)}
	}
	for i, ru := range r.Rules {
		out.Rules[i] = TierRuleOut(ru)
	}
	return out
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// RouterIn is a router's fields.
type RouterIn struct {
	Name              string        `json:"name"`
	Status            string        `json:"status,omitempty" doc:"shadow (default), active or disabled; active needs a plan with model routers"`
	Tiers             []TierOut     `json:"tiers" doc:"1 to 8, from cheapest to strongest"`
	Rules             []TierRuleOut `json:"rules,omitempty" doc:"At most 20"`
	DefaultTier       string        `json:"default_tier,omitempty" doc:"Default: the strongest tier"`
	QualityFloor      float64       `json:"quality_floor,omitempty" doc:"0.5 to 1 (default 0.95)"`
	SampleRate        float64       `json:"sample_rate,omitempty" doc:"0 (the mode's default) to 0.5"`
	EvalBudgetMicros  *int64        `json:"eval_budget_micros,omitempty" doc:"Default 100000000 ($100 a month); 0 turns evaluation off"`
	Judge             string        `json:"judge,omitempty"`
	DecisionModel     string        `json:"decision_model,omitempty"`
	DecisionTimeoutMS int           `json:"decision_timeout_ms,omitempty" doc:"20 to 2000 (default 150)"`
}

func (in *RouterIn) validate(v *muzak.Validation) {
	v.String(&in.Name).Trim().Required().MaxLen(64)
	v.Slice(&in.Tiers).MinItems(1).MaxItems(tiers.MaxTiers)
	v.Slice(&in.Rules).MaxItems(tiers.MaxRules)
}

func (in RouterIn) input() routers.Input {
	out := routers.Input{Name: in.Name, Status: in.Status, QualityFloor: in.QualityFloor, SampleRate: in.SampleRate,
		EvalBudgetMicros: routers.DefaultBudgetMicros, Judge: in.Judge, DecisionModel: in.DecisionModel,
		DecisionTimeoutMS: in.DecisionTimeoutMS, Spec: tiers.Spec{Default: in.DefaultTier}}
	if out.Status == "" {
		out.Status = tiers.Shadow
	}
	if in.EvalBudgetMicros != nil {
		out.EvalBudgetMicros = *in.EvalBudgetMicros
	}
	for _, t := range in.Tiers {
		out.Spec.Tiers = append(out.Spec.Tiers, tiers.Tier{Name: t.Name, Description: t.Description, Model: t.Model,
			Fallbacks: t.Fallbacks, Examples: t.Examples})
	}
	for _, r := range in.Rules {
		out.Spec.Rules = append(out.Spec.Rules, tiers.Rule(r))
	}
	return out
}

// CreateRouterIn adds a router.
type CreateRouterIn struct {
	platform.ProjectPath
	RouterIn
}

// Validate implements muzak.Validatable.
func (in *CreateRouterIn) Validate(v *muzak.Validation) { in.validate(v) }

// RouterPath names a router.
type RouterPath struct {
	platform.ProjectPath
	Router string `path:"router" doc:"Router ID (rtr_…)"`
}

// UpdateRouterIn replaces a router.
type UpdateRouterIn struct {
	RouterPath
	RouterIn
}

// Validate implements muzak.Validatable.
func (in *UpdateRouterIn) Validate(v *muzak.Validation) { in.validate(v) }

// ListRouters lists a project's routers.
func (h *Handlers) ListRouters(ctx *muzak.Context, _ platform.ProjectPath) (httpx.List[RouterOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	rs, err := h.Routers.List(ctx.Context(), pa.Project.ID)
	if err != nil {
		return httpx.List[RouterOut]{}, err
	}
	out := httpx.List[RouterOut]{Data: make([]RouterOut, len(rs))}
	for i, r := range rs {
		out.Data[i] = routerOut(r)
	}
	return out, nil
}

// planAllows refuses an active router on a plan without model routers:
// every plan may run them in shadow mode.
func (h *Handlers) planAllows(ctx *muzak.Context, status string) error {
	if status != tiers.Active {
		return nil
	}
	_, err := platform.RequireFeature(h.RouterDeps.Billing, h.RouterDeps.AppURL, catalog.Routers)(ctx)
	return err
}

// CreateRouter adds a router.
func (h *Handlers) CreateRouter(ctx *muzak.Context, in CreateRouterIn) (RouterOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	input := in.input()
	if err := h.planAllows(ctx, input.Status); err != nil {
		return RouterOut{}, err
	}
	r, err := h.Routers.Create(ctx.Context(), pa.OrgID(), pa.Project.ID, input)
	if err != nil {
		return RouterOut{}, routerFail(err)
	}
	return routerOut(r), nil
}

// GetRouter returns a router.
func (h *Handlers) GetRouter(ctx *muzak.Context, in RouterPath) (RouterOut, error) {
	r, err := h.router(ctx, in.Router)
	if err != nil {
		return RouterOut{}, err
	}
	return routerOut(r), nil
}

func (h *Handlers) router(ctx *muzak.Context, raw string) (*pdent.ModelRouter, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := ids.Decode(RouterPrefix, raw)
	if err != nil {
		return nil, httpx.NotFound("router")
	}
	r, err := h.Routers.Get(ctx.Context(), pa.Project.ID, id)
	if err != nil {
		return nil, routerFail(err)
	}
	return r, nil
}

// UpdateRouter replaces a router.
func (h *Handlers) UpdateRouter(ctx *muzak.Context, in UpdateRouterIn) (RouterOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := ids.Decode(RouterPrefix, in.Router)
	if err != nil {
		return RouterOut{}, httpx.NotFound("router")
	}
	input := in.input()
	if err := h.planAllows(ctx, input.Status); err != nil {
		return RouterOut{}, err
	}
	r, err := h.Routers.Update(ctx.Context(), pa.Project.ID, id, input)
	if err != nil {
		return RouterOut{}, routerFail(err)
	}
	return routerOut(r), nil
}

// DeleteRouter removes a router.
func (h *Handlers) DeleteRouter(ctx *muzak.Context, in RouterPath) (muzak.Empty, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := ids.Decode(RouterPrefix, in.Router)
	if err != nil {
		return muzak.Empty{}, httpx.NotFound("router")
	}
	return muzak.Empty{}, routerFail(h.Routers.Delete(ctx.Context(), pa.Project.ID, id))
}

// ExplainRouterIn is a request to place, as a client would send it.
type ExplainRouterIn struct {
	RouterPath
	APIFormat string          `json:"api_format,omitempty" doc:"openai.chat (default) or anthropic.messages"`
	Request   json.RawMessage `json:"request" doc:"The request body, as the client sends it to the gateway"`
	Tier      string          `json:"tier,omitempty" doc:"A tier the client names (model router:tier, or X-Promptdam-Tier)"`
	Route     string          `json:"route,omitempty"`
	User      string          `json:"user,omitempty"`
	Developer string          `json:"developer,omitempty"`
	Tags      []string        `json:"tags,omitempty"`
}

// Validate implements muzak.Validatable.
func (in *ExplainRouterIn) Validate(v *muzak.Validation) {
	v.Slice(&in.Tags).MaxItems(20)
}

// ExplainTierOut is whether a tier's model can take the call.
type ExplainTierOut struct {
	Name     string `json:"name"`
	Model    string `json:"model"`
	Provider string `json:"provider" doc:"The provider that would serve the tier's model first"`
	Serves   bool   `json:"serves"`
	Lacks    string `json:"lacks" doc:"What keeps its model from the call: no_provider, tools, images, files, json_schema or context"`
}

// ExplainTargetOut is a provider and model the gateway would try.
type ExplainTargetOut struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Translated bool   `json:"translated" doc:"The provider speaks the other API format: the call would be translated"`
}

// ExplainRouterOut is how the router would place the call.
type ExplainRouterOut struct {
	Tier        string  `json:"tier" doc:"The tier that would serve the call"`
	DecidedTier string  `json:"decided_tier" doc:"The tier the router chose (differs from tier in shadow mode)"`
	Source      string  `json:"source" doc:"explicit, rule, classifier, decision or default"`
	Rule        *int    `json:"rule" doc:"The rule that held, counted from 1"`
	Confidence  float64 `json:"confidence"`
	Moved       bool    `json:"moved" doc:"The tier first chosen couldn't take the call; the nearest that can did"`
	Shadow      bool    `json:"shadow"`
	Needs       struct {
		Tools      bool `json:"tools"`
		Images     bool `json:"images"`
		Files      bool `json:"files"`
		JSONSchema bool `json:"json_schema"`
		Tokens     int  `json:"tokens" doc:"Estimated input and answer limit; 0 when images make it unknown"`
	} `json:"needs"`
	Tiers   []ExplainTierOut   `json:"tiers"`
	Targets []ExplainTargetOut `json:"targets" doc:"Tried in order: the tier's model and fallbacks, then the stronger tiers'"`
}

// ExplainRouter places a request as the gateway would, without calling
// any provider.
func (h *Handlers) ExplainRouter(ctx *muzak.Context, in ExplainRouterIn) (ExplainRouterOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	row, err := h.router(ctx, in.Router)
	if err != nil {
		return ExplainRouterOut{}, err
	}
	format := wire.OpenAIChat
	switch in.APIFormat {
	case "", wire.OpenAIChat.String():
	case wire.AnthropicMessages.String():
		format = wire.AnthropicMessages
	default:
		return ExplainRouterOut{}, muzak.UnprocessableEntity("api_format is openai.chat or anthropic.messages").WithCode(CodeInvalidRouter)
	}
	req, err := wire.ParseRequest(format, in.Request)
	if err != nil {
		return ExplainRouterOut{}, muzak.UnprocessableEntity("request is not a " + format.String() + " request body: " + err.Error()).
			WithCode(CodeInvalidRouter)
	}
	d, err := h.RouterDeps.Sources.Build(ctx.Context(), pa.OrgID(), pa.Project.ID)
	if err != nil {
		return ExplainRouterOut{}, err
	}
	snap := snapshot.Plain(pa.Project.ID, d)
	rt := routers.Of(row)
	call := &gateway.Call{Format: format, Request: req, Shape: families.Of(req.Prefix, ""), BodyBytes: len(in.Request), Body: in.Request}
	meta := &resolver.Meta{Org: pa.OrgID(), Project: pa.Project.ID, Route: in.Route, User: in.User, Developer: in.Developer, Tags: in.Tags}
	pl, err := h.RouterDeps.Explainer.Explain(snap, &rt, in.Tier, call, meta)
	if err != nil {
		var ge *gateway.Error
		if errors.As(err, &ge) {
			return ExplainRouterOut{}, muzak.UnprocessableEntity(ge.Message).WithCode(ge.Code)
		}
		return ExplainRouterOut{}, err
	}
	spec := &rt.Spec
	out := ExplainRouterOut{Tier: spec.Tiers[pl.Served].Name, DecidedTier: spec.Tiers[pl.Choice.Tier].Name, Source: pl.Choice.Source,
		Confidence: pl.Choice.Confidence, Moved: pl.Choice.Moved, Shadow: pl.Shadow, Tiers: make([]ExplainTierOut, len(spec.Tiers)),
		Targets: make([]ExplainTargetOut, len(pl.Targets))}
	if pl.Choice.Rule >= 0 {
		n := pl.Choice.Rule + 1
		out.Rule = &n
	}
	out.Needs.Tools, out.Needs.Images, out.Needs.Files = pl.Needs.Tools, pl.Needs.Images, pl.Needs.Files
	out.Needs.JSONSchema, out.Needs.Tokens = pl.Needs.JSONSchema, pl.Needs.Tokens
	for i, t := range spec.Tiers {
		f := pl.Fits[i]
		et := ExplainTierOut{Name: t.Name, Model: t.Model, Serves: f.Lacks == "", Lacks: f.Lacks}
		if len(f.Targets) > 0 {
			et.Provider = f.Targets[0].Provider
		}
		out.Tiers[i] = et
	}
	for i, t := range pl.Targets {
		out.Targets[i] = ExplainTargetOut{Provider: t.Provider, Model: t.Model, Translated: t.Format != 0}
	}
	return out, nil
}

func routerFail(err error) error {
	var bad *routers.InvalidError
	var inUse *routers.InUseError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		e := muzak.UnprocessableEntity(bad.Reason).WithCode(CodeInvalidRouter)
		if len(bad.Problems) > 0 {
			ds := problems(bad.Problems)
			for i := range ds {
				ds[i].Field = fmt.Sprintf("rules[%d].when", bad.Rule)
			}
			e = e.WithDetails(ds...)
		}
		return e
	case errors.As(err, &inUse):
		return muzak.Conflict(inUse.Error()[len("routers: "):]).WithCode(CodeRouterInUse)
	case errors.Is(err, routers.ErrNotFound):
		return httpx.NotFound("router")
	case errors.Is(err, routers.ErrNameTaken):
		return muzak.Conflict("another router of the project has this name").WithCode(CodeRouterNameTaken)
	case errors.Is(err, routers.ErrLimit):
		return muzak.Conflict(fmt.Sprintf("a project has at most %d routers", routers.MaxPerProject)).WithCode(CodeRouterLimit)
	}
	return err
}
