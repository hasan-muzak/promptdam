package resolver

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/google/uuid"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/classifier"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/rules"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

// ruleRequest is a call as rules see it, routing rules and routers' alike.
func ruleRequest(c *gateway.Call, m *Meta) rules.Request {
	return rules.Request{Model: c.Request.Model, Format: c.Format.String(), Family: c.Shape.Family, User: m.User,
		Developer: m.Developer, Route: m.Route, InputTokens: c.BodyBytes / 4, Messages: c.Request.Messages,
		MaxTokens: c.Request.MaxTokens, HasTools: c.Request.Tools, JSON: c.Request.Prefix.Format != "",
		Stream: c.Request.Stream, Tags: m.Tags}
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
		p.vars = ruleRequest(p.c, p.m).Vars()
	}
	ok, err := prg.Bool(p.vars)
	return err == nil && ok
}

// placeCall puts a call in one of the router's tiers (tiers.Spec.Choose).
// learned lets the classifier place it. A shadow router decides but serves
// its default tier, unless the client named one. all checks every tier's
// fit, for explanations.
func (r *Resolver) placeCall(snap *snapshot.Snapshot, rt *routers.Router, pin string, c *gateway.Call, m *Meta, learned, all bool) (
	*Placement, error,
) {
	spec := &rt.Spec
	p := &placer{r: r, snap: snap, rt: rt, c: c, m: m, need: needsOf(c), fits: make([]Fit, len(spec.Tiers))}
	serves := func(i int) bool { return p.fit(i).Lacks == "" }
	call := tiers.Call{Pin: pin, Holds: p.holds, Serves: serves}
	if learned {
		call.Learned = func() (string, float64, bool) {
			cm := rt.Classifier
			if cm == nil {
				return "", 0, true // not trained yet
			}
			pl := cm.Place(classifier.Features(classifier.InputOf(c.Format, c.Request, c.Body, c.Shape.Family, m.Tags)))
			return pl.Tier, pl.P, pl.Unsure
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
	// The classifier places calls of active routers on plans with learned
	// routing; a shadow router's only say what it would do.
	learned := rt.Status == tiers.Shadow || r.learnedAllowed(ctx, m.Org)
	pl, err := r.placeCall(snap, rt, pin, c, m, learned, false)
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
	// A share of the router's calls is evaluated, while it has a budget
	// for it.
	if rt.EvalBudgetMicros > 0 && len(spec.Tiers) > 1 && r.cfg.Sample() < rt.Rate() {
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
	return r.placeCall(snap, rt, pin, c, m, true, true)
}

// learnedAllowed reports whether the organization's plan has learned
// routing; when it can't be read, the router keeps to its rules.
func (r *Resolver) learnedAllowed(ctx context.Context, org uuid.UUID) bool {
	if r.cfg.Entitlements == nil {
		return true
	}
	e, err := r.cfg.Entitlements(ctx, org)
	return err == nil && e.Has(catalog.RoutingLearned)
}
