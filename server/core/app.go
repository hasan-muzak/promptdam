package core

import (
	"context"
	"log/slog"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/config"
	"github.com/hasan-muzak/keel/pkg/keel"
	"github.com/hasan-muzak/keel/pkg/llm"

	pdent "github.com/hasan-muzak/keel/products/promptdam/server/ent"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/purge"
)

// App is Promptdam on top of keel: the shared services plus Promptdam's own
// graph and services.
type App struct {
	*keel.Core
	// Ent is Promptdam's product graph, on the same database as the core.
	Ent *pdent.Client
	// Prices cost model calls.
	Prices *llm.Catalog
	// GatewayURL is where clients reach the gateway, for setup instructions.
	GatewayURL string
	// Providers keeps projects' LLM providers and their keys.
	Providers *providers.Service
	// Keys keeps what the gateway applies to each API key.
	Keys *keysettings.Service
	// Purge deletes what Promptdam keeps of deleted organizations and
	// projects.
	Purge *purge.Service

	changed []func(ctx context.Context, project uuid.UUID)
}

// Option configures the App.
type Option func(*App)

// WithPrices uses catalog for model prices instead of the embedded one.
func WithPrices(catalog *llm.Catalog) Option {
	return func(a *App) { a.Prices = catalog }
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
	if a.Prices == nil {
		a.Prices = llm.Default()
	}
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
	// No foreign keys cross the graphs (ADR 0004): deleting an organization
	// or a project deletes Promptdam's rows in the deletion's transaction,
	// and enqueues the purge of its ClickHouse rows and Valkey keys with it.
	a.Purge = purge.New(purge.Config{Ent: a.Ent, Jobs: c.Jobs, Events: c.Events, KV: c.KV, Metering: c.Metering})
	c.Tenancy.OnOrgDeleted(a.Purge.OrgDeleted)
	c.Tenancy.OnProjectDeleted(a.Purge.ProjectDeleted)
	return a
}
