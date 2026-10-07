package prices

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/llm"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/priceoverride"
)

// MaxOverrides bounds a project's own prices: the gateway holds them all.
const MaxOverrides = 500

// maxPrice bounds a price per million tokens.
const maxPrice = 10_000

// Errors.
var (
	ErrNotFound = errors.New("prices: no such override")
	ErrLimit    = errors.New("prices: too many overrides")
)

// InvalidError is input that can't be saved; its message says why.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "prices: " + e.Reason }

// Override is a project's own price for a model, USD per million tokens.
type Override struct {
	// Model as providers report it; dated snapshots of it match too.
	Model string `json:"model"`
	// Provider is a provider's name in the project; empty for any.
	Provider    string  `json:"provider"`
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cached_input"`
	CacheWrite  float64 `json:"cache_write"`
	Output      float64 `json:"output"`
}

func (o *Override) check() error {
	o.Model, o.Provider = strings.TrimSpace(o.Model), strings.TrimSpace(o.Provider)
	if o.Model == "" || len(o.Model) > 200 || len(o.Provider) > 100 {
		return &InvalidError{Reason: "a model is 1 to 200 characters, a provider's name at most 100"}
	}
	for _, p := range []float64{o.Input, o.CachedInput, o.CacheWrite, o.Output} {
		if p < 0 || p > maxPrice || math.IsNaN(p) {
			return &InvalidError{Reason: fmt.Sprintf("a price is 0 to %d dollars per million tokens", maxPrice)}
		}
	}
	return nil
}

// Overrides keeps projects' own prices.
type Overrides struct {
	ent     *pdent.Client
	changed func(ctx context.Context, project uuid.UUID)
}

// NewOverrides returns the service; changed runs after a project's prices
// change.
func NewOverrides(ent *pdent.Client, changed func(ctx context.Context, project uuid.UUID)) *Overrides {
	if changed == nil {
		changed = func(context.Context, uuid.UUID) {}
	}
	return &Overrides{ent: ent, changed: changed}
}

// List returns a project's overrides.
func (s *Overrides) List(ctx context.Context, project uuid.UUID) ([]*pdent.PriceOverride, error) {
	return s.ent.PriceOverride.Query().Where(priceoverride.ProjectID(project)).
		Order(pdent.Asc(priceoverride.FieldModel), pdent.Asc(priceoverride.FieldProvider)).All(ctx)
}

// Put sets a project's price for a model (and provider), replacing any.
func (s *Overrides) Put(ctx context.Context, org, project uuid.UUID, o Override) (*pdent.PriceOverride, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	n, err := s.ent.PriceOverride.Query().Where(priceoverride.ProjectID(project)).Count(ctx)
	if err != nil {
		return nil, err
	}
	exists, err := s.ent.PriceOverride.Query().Where(priceoverride.ProjectID(project), priceoverride.Model(o.Model),
		priceoverride.Provider(o.Provider)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !exists && n >= MaxOverrides {
		return nil, ErrLimit
	}
	id, err := s.ent.PriceOverride.Create().SetOrgID(org).SetProjectID(project).SetModel(o.Model).SetProvider(o.Provider).
		SetInput(o.Input).SetCachedInput(o.CachedInput).SetCacheWrite(o.CacheWrite).SetOutput(o.Output).
		OnConflictColumns(priceoverride.FieldProjectID, priceoverride.FieldProvider, priceoverride.FieldModel).
		UpdateInput().UpdateCachedInput().UpdateCacheWrite().UpdateOutput().UpdateUpdatedAt().
		ID(ctx)
	if err != nil {
		return nil, err
	}
	s.changed(ctx, project)
	return s.ent.PriceOverride.Get(ctx, id)
}

// Delete removes an override.
func (s *Overrides) Delete(ctx context.Context, project, id uuid.UUID) error {
	n, err := s.ent.PriceOverride.Delete().Where(priceoverride.ID(id), priceoverride.ProjectID(project)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.changed(ctx, project)
	return nil
}

// All returns a project's overrides as the gateway holds them.
func (s *Overrides) All(ctx context.Context, project uuid.UUID) ([]Override, error) {
	rows, err := s.List(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]Override, len(rows))
	for i, r := range rows {
		out[i] = Override{Model: r.Model, Provider: r.Provider, Input: r.Input, CachedInput: r.CachedInput,
			CacheWrite: r.CacheWrite, Output: r.Output}
	}
	return out, nil
}

// Project is a project's own prices, by the provider they apply to (""
// for any).
type Project map[string]*llm.Catalog

// ProjectPrices groups overrides for lookups.
func ProjectPrices(os []Override) Project {
	if len(os) == 0 {
		return nil
	}
	p := Project{}
	for _, o := range os {
		c := p[o.Provider]
		if c == nil {
			c = &llm.Catalog{}
			p[o.Provider] = c
		}
		c.Models = append(c.Models, llm.Model{ID: strings.ToLower(o.Model), Provider: o.Provider, Input: o.Input,
			CachedInput: o.CachedInput, CacheWrite: o.CacheWrite, Output: o.Output})
	}
	return p
}

// Lookup finds the project's price for a model served by provider (the
// project's name for it): one set for that provider first, then one for
// any provider.
func (p Project) Lookup(provider, model string) (llm.Model, bool) {
	for _, key := range []string{provider, ""} {
		if c := p[key]; c != nil {
			if m, ok := c.Lookup("", model); ok {
				return m, true
			}
		}
	}
	return llm.Model{}, false
}
