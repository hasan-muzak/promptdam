package handlers

import (
	"slices"
	"strings"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

// ListModelsIn filters the models a project's providers serve.
type ListModelsIn struct {
	platform.ProjectPath
	Q         string `query:"q" doc:"Only models whose ID contains this, ignoring case"`
	Provider  string `query:"provider" doc:"Only those this provider serves (its name in the project)"`
	Feature   string `query:"feature" doc:"Only models known to take it: tools, images, files, json_schema or reasoning"`
	APIFormat string `query:"api_format" doc:"Only models a provider of this API format serves: openai or anthropic"`
}

// ModelPriceOut is a model's price, in USD per million tokens.
type ModelPriceOut struct {
	Input       float64 `json:"input" doc:"Input tokens not served from a cache"`
	CachedInput float64 `json:"cached_input" doc:"Input tokens read from the provider's cache"`
	CacheWrite  float64 `json:"cache_write" doc:"Input tokens written to the provider's cache"`
	Output      float64 `json:"output"`
}

func modelPrice(m llm.Model) *ModelPriceOut {
	return &ModelPriceOut{Input: m.Input, CachedInput: m.CachedInput, CacheWrite: m.CacheWrite, Output: m.Output}
}

// ModelOut is a model the project can call.
type ModelOut struct {
	ID            string         `json:"id" doc:"As clients name it: model, or provider/model for one provider"`
	Providers     []string       `json:"providers" doc:"The project's providers serving it, those of its API format first, then by priority"`
	APIFormat     string         `json:"api_format" doc:"The first provider's: openai or anthropic. Calls in the other format are translated"`
	ContextWindow int            `json:"context_window" doc:"Tokens; 0 when unknown"`
	MaxOutput     int            `json:"max_output" doc:"Tokens; 0 when unknown"`
	Features      []string       `json:"features" doc:"tools, images, files, json_schema, reasoning; null when the price list doesn't say"`
	ListPrice     *ModelPriceOut `json:"list_price" doc:"The price list's, at the first provider; null when it has none"`
	Price         *ModelPriceOut `json:"price" doc:"The project's own price, when it set one: it costs the model's calls"`
}

// OpenProviderOut is a provider that takes any model name.
type OpenProviderOut struct {
	Provider  string `json:"provider"`
	Kind      string `json:"kind"`
	APIFormat string `json:"api_format"`
	Note      string `json:"note" doc:"What names it takes"`
}

// ModelsOut are the models a project can call.
type ModelsOut struct {
	Data []ModelOut `json:"data"`
	// OpenProviders take any model name: pickers let the user type one.
	OpenProviders []OpenProviderOut `json:"open_providers" doc:"Providers that take any model name (a custom endpoint without a list, OpenRouter): type the model"`
}

// ListModels lists the models a project's providers serve, with their
// prices and what they take, for model pickers.
func (h *Handlers) ListModels(ctx *muzak.Context, in ListModelsIn) (ModelsOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	d, err := h.RouterDeps.Sources.Build(ctx.Context(), pa.OrgID(), pa.Project.ID)
	if err != nil {
		return ModelsOut{}, err
	}
	snap := snapshot.Plain(pa.Project.ID, d)
	served, open := h.RouterDeps.Explainer.ServedModels(snap, in.APIFormat)
	list := h.RouterDeps.Prices.Current()
	q := strings.ToLower(strings.TrimSpace(in.Q))
	out := ModelsOut{Data: []ModelOut{}, OpenProviders: []OpenProviderOut{}}
	for _, sm := range served {
		names := make([]string, 0, len(sm.Providers))
		formats := make([]string, 0, len(sm.Providers))
		for _, p := range sm.Providers {
			names, formats = append(names, p.Name), append(formats, p.Format)
		}
		switch {
		case q != "" && !strings.Contains(strings.ToLower(sm.ID), q),
			in.Provider != "" && !slices.Contains(names, in.Provider),
			in.APIFormat != "" && !slices.Contains(formats, in.APIFormat):
			continue
		}
		first := sm.Providers[0]
		m := ModelOut{ID: sm.ID, Providers: names, APIFormat: first.Format}
		catalogName := ""
		if kind, ok := providers.LookupKind(first.Kind); ok {
			catalogName = kind.Catalog
		}
		if cm, ok := list.Lookup(catalogName, sm.ID); ok {
			m.ContextWindow, m.MaxOutput, m.Features = int(cm.ContextWindow), int(cm.MaxOutput), cm.Features
			m.ListPrice = modelPrice(cm)
		}
		if in.Feature != "" {
			if has, known := (llm.Model{Features: m.Features}).Has(in.Feature); !has || !known {
				continue
			}
		}
		if own, ok := snap.Prices.Lookup(first.Name, sm.ID); ok {
			m.Price = modelPrice(own)
		}
		out.Data = append(out.Data, m)
	}
	for _, p := range open {
		if in.Provider != "" && p.Name != in.Provider || in.APIFormat != "" && p.Format != in.APIFormat {
			continue
		}
		note := "Any model name the endpoint accepts"
		if p.Kind == "openrouter" {
			note = "OpenRouter's vendor/model names, e.g. openai/gpt-5-mini"
		}
		out.OpenProviders = append(out.OpenProviders, OpenProviderOut{Provider: p.Name, Kind: p.Kind, APIFormat: p.Format, Note: note})
	}
	return out, nil
}
