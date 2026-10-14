package resolver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/google/uuid"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/classifier"
	"github.com/hasan-muzak/promptdam/server/features/conversation"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/rules"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

// RuleRequest is a call as rules see it: routing rules, routers' and
// firewall policies alike.
func RuleRequest(c *gateway.Call, m *Meta) rules.Request {
	t := c.Request.Turn
	return rules.Request{Model: c.Request.Model, Format: c.Format.String(), Family: c.Shape.Family, User: m.User,
		Developer: m.Developer, Route: m.Route, InputTokens: c.BodyBytes / 4, Messages: c.Request.Messages,
		MaxTokens: c.Request.MaxTokens, HasTools: c.Request.Tools, JSON: c.Request.Prefix.Format != "",
		Stream: c.Request.Stream, Tags: m.Tags, Turn: rules.Turn{Messages: t.Human + 1, InputTokens: t.ViewBytes / 4,
			HumanTurns: t.Humans, Continuation: t.Continuation}}
}

// Placement is how a router placed a call.
type Placement struct {
	Choice tiers.Choice
	// Served is the tier that serves the call: the chosen one, or the
	// default (as the call's needs allow) when a shadow router only
	// watches.
	Served int
	Shadow bool
	// Targets are tried in order: the served tier's model and fallbacks,
	// then the stronger tiers'. Calls never fall back to a weaker tier.
	Targets []gateway.Target
	// Fits say, for each tier the cascade looked at (every tier when
	// explaining), whether its model can take the call.
	Fits []Fit
	// Needs is what the call asks of a model.
	Needs Needs
}

// Fit is whether a tier's model can take a call.
type Fit struct {
	Checked bool
	// Targets are the tier's model and fallbacks, at the project's
	// providers.
	Targets []gateway.Target
	// Lacks is what keeps its model from the call: "no_provider", a
	// feature (tools, images, files, json_schema), "context", or "".
	Lacks string
}

// placer runs the cascade for one call, resolving tiers only as it reads
// them.
type placer struct {
	r    *Resolver
	snap *snapshot.Snapshot
	rt   *routers.Router
	c    *gateway.Call
	m    *Meta
	need Needs
	fits []Fit
	vars map[string]any
}

func (p *placer) fit(i int) *Fit {
	f := &p.fits[i]
	if f.Checked {
		return f
	}
	f.Checked = true
	t := p.rt.Spec.Tiers[i]
	ts := p.r.fallback(p.snap, p.c.Format, t.Model, nil)
	for _, fb := range t.Fallbacks {
		ts = append(ts, p.r.fallback(p.snap, p.c.Format, fb, ts)...)
	}
	f.Targets = ts
	switch md, ok := p.r.model(firstKind(ts), firstModel(ts)); {
	case len(ts) == 0:
		f.Lacks = "no_provider"
	case ok:
		f.Lacks = p.need.lack(md)
	}
	return f
}

func firstKind(ts []gateway.Target) string {
	if len(ts) == 0 {
		return ""
	}
	return ts[0].Kind
}

func firstModel(ts []gateway.Target) string {
	if len(ts) == 0 {
		return ""
	}
	return ts[0].Model
}

func (p *placer) holds(when string) bool {
	if p.r.cfg.Rules == nil {
		return false
	}
	prg, err := p.r.cfg.Rules.Compile(when)
	if err != nil {
		return false
	}
	if p.vars == nil {
		p.vars = RuleRequest(p.c, p.m).Vars()
	}
	ok, err := prg.Bool(p.vars)
	return err == nil && ok
}

// turnState is what a call's turn brings to its placement.
type turnState struct {
	// inherit is the turn's placement, for a call that continues it.
	inherit *tiers.Choice
	// continuing reports a call past its turn's human message: the
	// decision model is never asked mid-turn.
	continuing bool
	// provider and model served the turn's latest call: within its tier,
	// that target goes first (its prompt cache is warm there).
	provider, model string
}

// placeCall puts a call in one of the router's tiers (tiers.Spec.Choose).
// learned lets the classifier place it. A shadow router decides but serves
// its default tier, unless the client named one. all checks every tier's
// fit, for explanations. turn is the call's turn (nil: none, or an
// explanation).
func (r *Resolver) placeCall(ctx context.Context, snap *snapshot.Snapshot, rt *routers.Router, pin string, c *gateway.Call, m *Meta,
	learned, all bool, turn *turnState,
) (*Placement, error) {
	spec := &rt.Spec
	p := &placer{r: r, snap: snap, rt: rt, c: c, m: m, need: needsOf(c), fits: make([]Fit, len(spec.Tiers))}
	serves := func(i int) bool { return p.fit(i).Lacks == "" }
	call := tiers.Call{Pin: pin, Holds: p.holds, Serves: serves}
	if turn != nil {
		call.Inherit = turn.inherit
	}
	if len(rt.Demoted) > 0 {
		call.Demoted = func(i int) bool { return rt.IsDemoted(spec.Tiers[i].Name) }
	}
	if learned {
		call.Learned = func() (string, float64, bool) {
			cm := rt.Classifier
			if cm == nil {
				return "", 0, true // not trained yet
			}
			pl := cm.Place(classifier.Features(classifier.InputOf(c.Request, c.Shape.Family, m.Tags)))
			return pl.Tier, pl.P, pl.Unsure
		}
		// The decision model answers for active routers only, never when
		// explaining (a call it would wait on must change what serves it),
		// and never mid-turn: a turn's calls keep its tier.
		if rt.DecisionModel != "" && rt.Status == tiers.Active && !all && r.cfg.Decider != nil && (turn == nil || !turn.continuing) {
			call.Decide = func() string { return r.decide(ctx, snap, rt, c, m) }
		}
	}
	choice, err := spec.Choose(call)
	if errors.Is(err, tiers.ErrUnknownTier) {
		return nil, refuse(http.StatusBadRequest, gateway.TypeInvalidRequest, "unknown_tier",
			"router "+rt.Name+" has no tier "+strconv.Quote(pin))
	}
	pl := &Placement{Choice: choice, Served: choice.Tier,
		Shadow: rt.Status == tiers.Shadow && choice.Source != tiers.SourceExplicit}
	if pl.Shadow {
		// The default tier, moved only by what the call needs.
		base, _ := spec.Choose(tiers.Call{Serves: serves})
		pl.Served = base.Tier
	}
	for i := pl.Served; i < len(spec.Tiers); i++ {
		for _, t := range p.fit(i).Targets {
			if !tried(pl.Targets, t.Provider, t.Model) {
				pl.Targets = append(pl.Targets, t)
			}
		}
	}
	if turn != nil && turn.provider != "" {
		preferFirst(pl.Targets[:min(len(p.fit(pl.Served).Targets), len(pl.Targets))], turn.provider, turn.model)
	}
	if all {
		for i := range spec.Tiers {
			p.fit(i)
		}
	}
	pl.Fits, pl.Needs = p.fits, p.need
	return pl, nil
}

// place puts a call in one of the router's tiers and returns its targets
// and the headers that say how. named reports that the call named the
// router as its model, so savings count against the default tier rather
// than the model asked for.
func (r *Resolver) place(ctx context.Context, snap *snapshot.Snapshot, rt *routers.Router, pin string, named bool, c *gateway.Call,
	m *Meta,
) ([]gateway.Target, http.Header, error) {
	// An active router serves traffic on plans with model routers; on
	// another (a downgrade), it only watches, as in shadow mode, until the
	// plan has them again.
	if rt.Status == tiers.Active && !r.planHas(ctx, m.Org, catalog.Routers) {
		watching := *rt
		watching.Status = tiers.Shadow
		rt = &watching
	}
	// The classifier places calls of active routers on plans with learned
	// routing; a shadow router's only say what it would do.
	learned := rt.Status == tiers.Shadow || r.planHas(ctx, m.Org, catalog.RoutingLearned)
	turn := r.turnOf(ctx, rt, m)
	pl, err := r.placeCall(ctx, snap, rt, pin, c, m, learned, false, turn)
	if err != nil {
		return nil, nil, err
	}
	spec := &rt.Spec
	if len(pl.Targets) == 0 {
		return nil, nil, refuse(http.StatusNotFound, gateway.TypeNotFound, "no_provider",
			"no provider of the project serves tier "+spec.Tiers[pl.Served].Name+" of router "+rt.Name+
				" or a stronger one: add one, or list their models on a provider")
	}
	m.Router, m.Tier, m.Decided, m.Shadow = rt.Name, spec.Tiers[pl.Served].Name, spec.Tiers[pl.Choice.Tier].Name, pl.Shadow
	m.TierSource, m.TierConfidence = pl.Choice.Source, pl.Choice.Confidence
	if named {
		if f := &pl.Fits[spec.Index(spec.Default)]; f.Checked && len(f.Targets) > 0 {
			m.Baseline = f.Targets[0]
		} else if base := r.fallback(snap, c.Format, spec.Tiers[spec.Index(spec.Default)].Model, nil); len(base) > 0 {
			m.Baseline = base[0]
		}
	}
	// A turn's placement holds for the calls that continue it, on every
	// replica: the first placement of a turn is kept.
	if turn != nil && turn.inherit == nil && r.cfg.Turns != nil {
		r.cfg.Turns.Place(m.Project, m.Turn.Turn, conversation.Placement{Tier: m.Tier, Decided: m.Decided,
			Source: pl.Choice.Source, Confidence: pl.Choice.Confidence, Version: rt.Version()})
	}
	// A share of the router's calls is evaluated, while it has a budget
	// for it: calls that start their turn, whose features are the turn's
	// as the classifier reads it (a tool loop's later calls answer tool
	// results, not the human).
	if rt.EvalBudgetMicros > 0 && len(spec.Tiers) > 1 && (!m.InTurn || !m.Turn.Continuation) && r.cfg.Sample() < rt.Rate() {
		m.Sampled, m.Placed, m.Snap = true, rt, snap
	}
	source := pl.Choice.Source
	if pl.Shadow {
		source = "shadow:" + source
	}
	h := http.Header{}
	h.Set(brand.HeaderRouter, rt.Name)
	h.Set(brand.HeaderTier, m.Tier)
	h.Set(brand.HeaderTierSource, source)
	return pl.Targets, h, nil
}

// turnOf is what a router call's turn brings to its placement: for a call
// that continues its turn, the turn's placement (when this version of the
// router made it) and the target that served its latest call.
func (r *Resolver) turnOf(ctx context.Context, rt *routers.Router, m *Meta) *turnState {
	if !m.InTurn {
		return nil
	}
	ts := &turnState{continuing: m.Turn.Continuation}
	if !ts.continuing || r.cfg.Turns == nil {
		return ts
	}
	rec, ok := r.cfg.Turns.Get(ctx, m.Project, m.Turn.Turn)
	if !ok {
		return ts
	}
	if rec.Placed() && rec.Version == rt.Version() {
		if i := rt.Spec.Index(rec.Decided); i >= 0 {
			ts.inherit = &tiers.Choice{Tier: i, Source: rec.Source, Confidence: rec.Confidence}
		}
	}
	ts.provider, ts.model = rec.Provider, rec.Model
	return ts
}

// preferFirst moves the target of provider's model to the front, keeping
// the others' order.
func preferFirst(ts []gateway.Target, provider, model string) {
	for j, t := range ts {
		if t.Provider == provider && t.Model == model {
			copy(ts[1:j+1], ts[:j])
			ts[0] = t
			return
		}
	}
}

// Model is what the price list knows of a provider kind's model: its
// features and limits.
func (r *Resolver) Model(kind, model string) (llm.Model, bool) { return r.model(kind, model) }

// Targets are where a model ("model" or "provider/model") is served for a
// call of format f, by the project's providers.
func (r *Resolver) Targets(snap *snapshot.Snapshot, f wire.Format, model string) []gateway.Target {
	return r.fallback(snap, f, model, nil)
}

// Explain places a call as the gateway would, checking every tier, for
// the routers API. snap may be built without opening providers' keys.
func (r *Resolver) Explain(snap *snapshot.Snapshot, rt *routers.Router, pin string, c *gateway.Call, m *Meta) (*Placement, error) {
	return r.placeCall(context.Background(), snap, rt, pin, c, m, true, true, nil)
}

// decide asks the router's decision model, on the project's own key, which
// tier the call needs ("" when it can't say in time).
func (r *Resolver) decide(ctx context.Context, snap *snapshot.Snapshot, rt *routers.Router, c *gateway.Call, m *Meta) string {
	if m.Firewall != nil && m.Firewall.Blocked {
		return "" // what the firewall blocks goes to no provider
	}
	ts := r.fallback(snap, wire.OpenAIChat, rt.DecisionModel, nil)
	if len(ts) == 0 {
		return ""
	}
	// The decision model reads the call: not when a policy would keep the
	// call from its provider.
	if o := m.Firewall; o != nil && snap.Firewall.HasPolicies() {
		asked := *o
		snap.Firewall.Decide(&asked, RuleRequest(c, m), r.policyCall(ctx, snap, m,
			firewall.Target{Provider: ts[0].Provider, Kind: ts[0].Kind, Model: ts[0].Model}))
		if asked.Blocked || asked.RouteTo != "" {
			return ""
		}
	}
	md, known := r.model(ts[0].Kind, ts[0].Model)
	schema, _ := md.Has(llm.FeatureJSONSchema)
	q := Question{Project: m.Project, Router: rt.Name, Family: c.Shape.Family, Target: ts[0], Schema: schema || !known || md.Features == nil,
		Tiers: rt.Spec.Tiers, Instructions: c.Request.Prefix.System, Request: c.Request.Turn.Text,
		Timeout: time.Duration(rt.DecisionTimeoutMS) * time.Millisecond}
	tier, usage, err := r.cfg.Decider.Decide(ctx, q)
	m.Decision.Target, m.Decision.Usage = ts[0], usage
	if err != nil {
		return ""
	}
	return tier
}

// planHas reports whether the organization's plan has a feature; when the
// plan can't be read, it hasn't (routers then watch and keep to rules).
func (r *Resolver) planHas(ctx context.Context, org uuid.UUID, feature string) bool {
	if r.cfg.Entitlements == nil {
		return true
	}
	e, err := r.cfg.Entitlements(ctx, org)
	return err == nil && e.Has(feature)
}
