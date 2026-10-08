package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
)

// PricePrefix starts price override IDs: prc_….
const PricePrefix = "prc"

// Error codes of the price routes.
const (
	CodeInvalidPrice = "invalid_price"
	CodePriceLimit   = "price_limit"
)

// PriceOut is a project's own price for a model.
type PriceOut struct {
	ID          string  `json:"id" doc:"Price ID (prc_…)"`
	Model       string  `json:"model"`
	Provider    string  `json:"provider" doc:"A provider's name in the project; empty for any"`
	Input       float64 `json:"input" doc:"USD per million input tokens not served from a cache"`
	CachedInput float64 `json:"cached_input" doc:"USD per million input tokens read from the provider's cache"`
	CacheWrite  float64 `json:"cache_write" doc:"USD per million input tokens written to the provider's cache"`
	Output      float64 `json:"output" doc:"USD per million output tokens"`
	// ListPrice compares the project's price with the price list's.
	ListPrice *ModelPriceOut `json:"list_price" doc:"The price list's for the model (at the provider's kind); null when it has none"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func priceOut(p *pdent.PriceOverride, list *ModelPriceOut) PriceOut {
	return PriceOut{ID: ids.Encode(PricePrefix, p.ID), Model: p.Model, Provider: p.Provider, Input: p.Input,
		CachedInput: p.CachedInput, CacheWrite: p.CacheWrite, Output: p.Output, ListPrice: list, UpdatedAt: p.UpdatedAt}
}

// listPrices returns the price list's price of a model at one of the
// project's providers (by name; "" for any).
func (h *Handlers) listPrices(ctx *muzak.Context, project uuid.UUID) (func(provider, model string) *ModelPriceOut, error) {
	ps, err := h.Providers.List(ctx.Context(), project)
	if err != nil {
		return nil, err
	}
	kinds := make(map[string]string, len(ps))
	for _, p := range ps {
		kinds[p.Name] = p.Kind
	}
	list := h.RouterDeps.Prices.Current()
	return func(provider, model string) *ModelPriceOut {
		catalogName := ""
		if k, ok := providers.LookupKind(kinds[provider]); ok {
			catalogName = k.Catalog
		}
		if m, ok := list.Lookup(catalogName, model); ok {
			return modelPrice(m)
		}
		return nil
	}, nil
}

// PutPriceIn sets a project's own price for a model.
type PutPriceIn struct {
	platform.ProjectPath
	Model       string   `json:"model" doc:"As providers report it; its dated snapshots match too"`
	Provider    string   `json:"provider,omitempty" doc:"A provider's name in the project; omit for any"`
	Input       float64  `json:"input"`
	CachedInput *float64 `json:"cached_input,omitempty" doc:"Default: the input price"`
	CacheWrite  *float64 `json:"cache_write,omitempty" doc:"Default: the input price"`
	Output      float64  `json:"output"`
}

// Validate implements muzak.Validatable.
func (in *PutPriceIn) Validate(v *muzak.Validation) {
	v.String(&in.Model).Trim().Required().MaxLen(200)
	v.String(&in.Provider).Trim().MaxLen(100)
}

// ListPrices lists a project's own prices.
func (h *Handlers) ListPrices(ctx *muzak.Context, _ platform.ProjectPath) (httpx.List[PriceOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	rows, err := h.Prices.List(ctx.Context(), pa.Project.ID)
	if err != nil {
		return httpx.List[PriceOut]{}, err
	}
	listPrice, err := h.listPrices(ctx, pa.Project.ID)
	if err != nil {
		return httpx.List[PriceOut]{}, err
	}
	out := httpx.List[PriceOut]{Data: make([]PriceOut, len(rows))}
	for i, r := range rows {
		out.Data[i] = priceOut(r, listPrice(r.Provider, r.Model))
	}
	return out, nil
}

// PutPrice sets a project's own price for a model (and provider).
func (h *Handlers) PutPrice(ctx *muzak.Context, in PutPriceIn) (PriceOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	or := func(p *float64) float64 {
		if p == nil {
			return in.Input
		}
		return *p
	}
	p, err := h.Prices.Put(ctx.Context(), pa.OrgID(), pa.Project.ID, prices.Override{
		Model: in.Model, Provider: in.Provider, Input: in.Input, CachedInput: or(in.CachedInput), CacheWrite: or(in.CacheWrite),
		Output: in.Output,
	})
	if err != nil {
		return PriceOut{}, priceFail(err)
	}
	listPrice, err := h.listPrices(ctx, pa.Project.ID)
	if err != nil {
		return PriceOut{}, err
	}
	return priceOut(p, listPrice(p.Provider, p.Model)), nil
}

// MissingPricesIn asks for the models a project's calls had no price for.
type MissingPricesIn struct {
	platform.ProjectPath
	TimeRange
}

// MissingPriceOut is a model calls were served by without any price.
type MissingPriceOut struct {
	Model        string    `json:"model" doc:"As the provider reported it"`
	Provider     string    `json:"provider" doc:"The provider's name in the project"`
	Kind         string    `json:"kind"`
	Calls        uint64    `json:"calls"`
	InputTokens  uint64    `json:"input_tokens"`
	OutputTokens uint64    `json:"output_tokens"`
	LastSeen     time.Time `json:"last_seen"`
}

// MissingPrices lists the models a project's calls were served by without
// a price, the busiest first: their spend and savings are unknown until
// the project or the price list prices them.
func (h *Handlers) MissingPrices(ctx *muzak.Context, in MissingPricesIn) (httpx.List[MissingPriceOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := in.bounds(7 * 24 * time.Hour)
	if err != nil {
		return httpx.List[MissingPriceOut]{}, err
	}
	rows, err := h.Analytics.Unpriced(ctx.Context(), pa.Project.ID, from, to)
	if err != nil {
		return httpx.List[MissingPriceOut]{}, analyticsFail(err)
	}
	own, err := h.Prices.All(ctx.Context(), pa.Project.ID)
	if err != nil {
		return httpx.List[MissingPriceOut]{}, err
	}
	project := prices.ProjectPrices(own)
	out := httpx.List[MissingPriceOut]{Data: []MissingPriceOut{}}
	for _, r := range rows {
		// Priced since, by the project or the price list.
		if _, ok := requestlog.Price(h.RouterDeps.Prices, project, r.Provider, r.Kind, r.Model); ok {
			continue
		}
		out.Data = append(out.Data, MissingPriceOut{Model: r.Model, Provider: r.Provider, Kind: r.Kind, Calls: r.Calls,
			InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, LastSeen: r.LastSeen.UTC()})
	}
	return out, nil
}

// PricePath names a price.
type PricePath struct {
	platform.ProjectPath
	Price string `path:"price" doc:"Price ID (prc_…)"`
}

// DeletePrice removes a project's own price: the price list's applies again.
func (h *Handlers) DeletePrice(ctx *muzak.Context, in PricePath) (muzak.Empty, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := ids.Decode(PricePrefix, in.Price)
	if err != nil {
		return muzak.Empty{}, httpx.NotFound("price")
	}
	return muzak.Empty{}, priceFail(h.Prices.Delete(ctx.Context(), pa.Project.ID, id))
}

func priceFail(err error) error {
	var bad *prices.InvalidError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return muzak.UnprocessableEntity(bad.Reason).WithCode(CodeInvalidPrice)
	case errors.Is(err, prices.ErrNotFound):
		return httpx.NotFound("price")
	case errors.Is(err, prices.ErrLimit):
		return muzak.Conflict("a project has at most 500 prices of its own").WithCode(CodePriceLimit)
	}
	return err
}
