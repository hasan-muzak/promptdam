package handlers

import (
	"errors"
	"strings"
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

// SuggestRouterIn shapes a suggested router.
type SuggestRouterIn struct {
	platform.ProjectPath
	Name         string `query:"name" doc:"The router's name (default auto)"`
	Tiers        int    `query:"tiers" doc:"2 (fast, expert) or 3 (fast, standard, expert; default)"`
	CurrentModel string `query:"current_model" doc:"The model the project uses today, made the default tier; default: the one its calls used most in the last 7 days"`
}

// Validate implements muzak.Validatable.
func (in *SuggestRouterIn) Validate(v *muzak.Validation) {
	v.String(&in.Name).Trim().MaxLen(64)
	v.String(&in.CurrentModel).Trim().MaxLen(300)
}

// SuggestTierOut says how a suggested tier was picked.
type SuggestTierOut struct {
	Name             string  `json:"name"`
	Provider         string  `json:"provider" doc:"The provider of the tier's model"`
	InputPerMillion  float64 `json:"input_per_million" doc:"USD per million input tokens, at the project's prices"`
	OutputPerMillion float64 `json:"output_per_million"`
	Picked           string  `json:"picked" doc:"family (the newest of a known model family), price (by price among the provider's models) or current (the model used most)"`
}

// SuggestRouterOut is a router to start from.
type SuggestRouterOut struct {
	Router RouterIn         `json:"router" doc:"Ready to create as is (POST …/routers, in shadow mode), or to edit first"`
	Tiers  []SuggestTierOut `json:"tiers"`
	Notes  []string         `json:"notes"`
}

// SuggestRouter suggests a router built from the project's providers'
// models.
func (h *Handlers) SuggestRouter(ctx *muzak.Context, in SuggestRouterIn) (SuggestRouterOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	deps := h.RouterDeps
	d, err := deps.Sources.Build(ctx.Context(), pa.OrgID(), pa.Project.ID)
	if err != nil {
		return SuggestRouterOut{}, err
	}
	snap := snapshot.Plain(pa.Project.ID, d)
	cands := h.candidates(snap)

	var current *routers.Candidate
	model, provider := in.CurrentModel, ""
	if model == "" && deps.Events != nil {
		model, provider = h.mostUsed(ctx, snap)
	}
	if model != "" {
		for _, c := range cands {
			if (provider == "" || c.Provider == provider) && (c.Model.ID == model || c.Provider+"/"+c.Model.ID == model) {
				current = &c
				break
			}
		}
		if current == nil {
			// A model the providers serve but don't list (a dated name): its
			// first target, priced.
			if ts := deps.Explainer.Targets(snap, wire.OpenAIChat, model); len(ts) > 0 {
				if p, ok := requestlog.Price(deps.Prices, snap.Prices, ts[0].Provider, ts[0].Kind, ts[0].Model); ok {
					if p.ID == "" {
						p.ID = ts[0].Model
					}
					current = &routers.Candidate{Provider: ts[0].Provider, Kind: ts[0].Kind, Model: p}
				}
			}
		}
	}

	n := in.Tiers
	if n != 2 {
		n = 3
	}
	s, err := routers.Suggest(n, cands, current)
	if errors.Is(err, routers.ErrNoModels) {
		return SuggestRouterOut{}, muzak.UnprocessableEntity("none of the project's providers serves a priced chat model: add one, " +
			"or list its models and set their prices").WithCode(CodeInvalidRouter)
	}
	if err != nil {
		return SuggestRouterOut{}, err
	}
	name := in.Name
	if name == "" {
		name = "auto"
	}
	out := SuggestRouterOut{Router: RouterIn{Name: name, Status: "shadow", DefaultTier: s.Spec.Default, Judge: s.Judge,
		Tiers: make([]TierOut, len(s.Spec.Tiers))}, Notes: s.Notes}
	for i, t := range s.Spec.Tiers {
		out.Router.Tiers[i] = TierOut{Name: t.Name, Description: t.Description, Model: t.Model, Fallbacks: orEmpty(t.Fallbacks),
			Examples: []string{}, Effort: t.Effort}
	}
	for _, p := range s.Picks {
		out.Tiers = append(out.Tiers, SuggestTierOut{Name: p.Tier, Provider: p.Provider, InputPerMillion: p.Input,
			OutputPerMillion: p.Output, Picked: p.How})
	}
	return out, nil
}

// candidates are the models the project's providers serve, in their
// priority order: those a provider lists, else the price list's models of
// its kind, priced at the project's prices.
func (h *Handlers) candidates(snap *snapshot.Snapshot) []routers.Candidate {
	deps := h.RouterDeps
	list := deps.Prices.Current()
	var out []routers.Candidate
	for _, p := range snap.Providers {
		kind, _ := providers.LookupKind(p.Kind)
		ids := make([]string, 0, len(p.Models))
		for _, id := range p.Models {
			if !strings.HasSuffix(id, "*") {
				ids = append(ids, id)
			}
		}
		if len(p.Models) == 0 && kind.Catalog != "" {
			for _, m := range list.Models {
				if m.Provider == kind.Catalog {
					ids = append(ids, m.ID)
				}
			}
		}
		for _, id := range ids {
			m, ok := requestlog.Price(deps.Prices, snap.Prices, p.Name, p.Kind, id)
			if !ok {
				continue
			}
			m.ID = id
			out = append(out, routers.Candidate{Provider: p.Name, Kind: p.Kind, Model: m})
		}
	}
	return out
}

// mostUsed is the model (and its provider) the project's calls used most
// in the last 7 days, from the request log's rollup.
func (h *Handlers) mostUsed(ctx *muzak.Context, snap *snapshot.Snapshot) (model, provider string) {
	var rows []struct {
		Provider string `ch:"provider"`
		Model    string `ch:"served_model"`
		N        uint64 `ch:"n"`
	}
	err := h.RouterDeps.Events.Select(ctx.Context(), &rows, events.Project(snap.Project), `SELECT provider, served_model,
		sum(requests) AS n FROM requests_hourly WHERE {tenant} AND hour >= ? AND served_model != ''
		GROUP BY provider, served_model ORDER BY n DESC LIMIT 1`, time.Now().Add(-7*24*time.Hour))
	if err != nil || len(rows) == 0 {
		return "", ""
	}
	return rows[0].Model, rows[0].Provider
}
