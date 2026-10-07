package core

import (
	"context"
	"log/slog"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/config"
	"github.com/hasan-muzak/keel/pkg/counter"
	"github.com/hasan-muzak/keel/pkg/keel"
	"github.com/hasan-muzak/keel/pkg/llm"

	pdent "github.com/hasan-muzak/keel/products/promptdam/server/ent"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/budgets"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/prices"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/purge"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/routes"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/snapshot"
)

// App is Promptdam on top of keel: the shared services plus Promptdam's own
// graph and services.
type App struct {
	*keel.Core
	// Ent is Promptdam's product graph, on the same database as the core.
	Ent *pdent.Client
	// Prices cost model calls: the price list synced daily (the embedded or
	// configured catalog until the first sync).
	Prices *prices.Store
	// PriceOverrides are projects' own prices for models.
	PriceOverrides *prices.Overrides
	// GatewayURL is where clients reach the gateway, for setup instructions.
	GatewayURL string
	// Providers keeps projects' LLM providers and their keys.
	Providers *providers.Service
	// Keys keeps what the gateway applies to each API key.
	Keys *keysettings.Service
	// Routes keeps the paths requests take: fallbacks and retries.
	Routes *routes.Service
	// Spend counts what each scope spends (the gateway adds to it, starting
	// its lifecycle); Budgets cap it.
	Spend         *budgets.Spend
	SpendCounters *counter.Counters
	Budgets       *budgets.Service
	// Purge deletes what Promptdam keeps of deleted organizations and
	// projects.
	Purge *purge.Service

	changed  []func(ctx context.Context, project uuid.UUID)
	fallback *llm.Catalog
}

// Option configures the App.
type Option func(*App)

// WithPrices costs calls with catalog until a price list is synced,
// instead of the embedded catalog.
func WithPrices(catalog *llm.Catalog) Option {
	return func(a *App) { a.fallback = catalog }
}

// WithGatewayURL sets where clients reach the gateway.
func WithGatewayURL(url string) Option {
	return func(a *App) { a.GatewayURL = url }
}

// OnChanged registers a function run after a project's gateway
// configuration (providers, keys, routes, budgets) changes.
func (a *App) OnChanged(fn func(ctx context.Context, project uuid.UUID)) {
	a.changed = append(a.changed, fn)
}

func (a *App) configChanged(ctx context.Context, project uuid.UUID) {
	for _, fn := range a.changed {
		fn(ctx, project)
	}
}

// NewApp wraps keel's services with Promptdam's and registers its hooks.
func NewApp(c *keel.Core, opts ...Option) *App {
	a := &App{Core: c, Ent: pdent.NewClient(pdent.Driver(entsql.OpenDB(dialect.Postgres, c.DB.SQL)))}
	for _, o := range opts {
		o(a)
	}
	a.Prices = prices.New(prices.Config{Ent: a.Ent, KV: c.KV, Fallback: a.fallback})
	a.PriceOverrides = prices.NewOverrides(a.Ent, a.configChanged)
	if a.GatewayURL == "" {
		a.GatewayURL = "http://localhost:8282"
	}
	a.Providers = providers.New(providers.Config{
		Ent: a.Ent, Vault: c.Vault, AllowLoopback: c.Config.Env == config.Development, Changed: a.configChanged,
	})
	a.Keys = keysettings.New(keysettings.Config{Ent: a.Ent, Key: c.APIKeys.Get, Scope: ScopeGateway, Changed: a.configChanged})
	c.APIKeys.OnRotated(func(ctx context.Context, org, old, replacement uuid.UUID) {
		if err := a.Keys.Rotated(ctx, org, old, replacement); err != nil {
			slog.Error("promptdam: carrying a rotated key's gateway settings over; set them again on the replacement",
				"key", replacement, "err", err)
		}
	})
	a.Routes = routes.New(a.Ent, a.configChanged)
	a.SpendCounters = counter.New(c.KV, "spend", counter.Options{})
	a.Spend = budgets.NewSpend(a.SpendCounters)
	a.Budgets = budgets.New(budgets.Config{
		Ent: a.Ent, Spend: a.Spend, Notifier: c.Notifier, Webhooks: c.Webhooks, AppURL: c.Config.AppURL, Changed: a.configChanged,
	})
	// Every change to a project's gateway configuration reaches the
	// gateways' snapshots.
	a.OnChanged(snapshot.NewInvalidator(c.KV).Changed)
	// No foreign keys cross the graphs (ADR 0004): deleting an organization
	// or a project deletes Promptdam's rows in the deletion's transaction,
	// and enqueues the purge of its ClickHouse rows and Valkey keys with it.
	a.Purge = purge.New(purge.Config{Ent: a.Ent, Jobs: c.Jobs, Events: c.Events, KV: c.KV, Metering: c.Metering})
	c.Tenancy.OnOrgDeleted(a.Purge.OrgDeleted)
	c.Tenancy.OnProjectDeleted(a.Purge.ProjectDeleted)
	return a
}

// Sources are where the gateway's snapshots are built from.
func (a *App) Sources() snapshot.Sources {
	return snapshot.Sources{Providers: a.Providers, Keys: a.Keys, Routes: a.Routes, Budgets: a.Budgets, Prices: a.PriceOverrides}
}
