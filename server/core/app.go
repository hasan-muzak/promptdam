package core

import (
	"context"
	"log/slog"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/config"
	"github.com/hasan-muzak/keel-core/pkg/counter"
	"github.com/hasan-muzak/keel-core/pkg/keel"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/policy"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/features/activity"
	"github.com/hasan-muzak/promptdam/server/features/alerts"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/promptdam/server/features/payloads"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/purge"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
	"github.com/hasan-muzak/promptdam/server/features/rules"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
	"github.com/hasan-muzak/promptdam/server/features/training"
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
	// Rules keeps routing rules; RuleEnv compiles them, and model routers'
	// rules too.
	Rules   *rules.Service
	RuleEnv *policy.Env
	// Routers keeps model routers: tiers of models a call is placed in;
	// Training trains their classifiers on their judged calls.
	Routers  *routers.Service
	Training *training.Service
	// Payloads keeps whether projects keep their calls' payloads.
	Payloads *payloads.Service
	// Activity keeps what happened to each project that was worth telling.
	Activity *activity.Service
	// Alerts keeps alert rules; AlertsEval evaluates them every minute.
	Alerts     *alerts.Service
	AlertsEval *alerts.Evaluator
	// Firewall keeps projects' firewalls: what their calls' secrets and
	// personal data get, and their policies; FirewallWatch tells
	// organizations what they did.
	Firewall      *firewall.Service
	FirewallWatch *firewall.Watcher
	// Watcher announces ready routers and demotes drifting tiers.
	Watcher *routers.Watcher
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
	env, err := rules.NewEnv()
	if err != nil {
		panic(err) // the environment is declared here, never from input
	}
	a.RuleEnv = env
	a.Rules = rules.New(a.Ent, env, a.configChanged)
	a.Routers = routers.New(a.Ent, env, a.Prices, a.configChanged)
	a.Activity = activity.New(a.Ent)
	a.Payloads = payloads.NewService(a.Ent, a.configChanged)
	a.Alerts = alerts.New(a.Ent)
	a.AlertsEval = alerts.NewEvaluator(alerts.EvalConfig{Ent: a.Ent, Events: c.Events, Notifier: c.Notifier, Webhooks: c.Webhooks,
		Activity: a.Activity, Entitlements: c.Metering.Entitlements, AppURL: c.Config.AppURL})
	a.Firewall = firewall.NewService(a.Ent, a.configChanged)
	a.FirewallWatch = firewall.NewWatcher(firewall.WatchConfig{Events: c.Events, KV: c.KV, Notifier: c.Notifier,
		Webhooks: c.Webhooks, Activity: a.Activity, AppURL: c.Config.AppURL, ProjectName: func(ctx context.Context, id uuid.UUID) (string, error) {
			p, err := c.Tenancy.ProjectByID(ctx, id)
			if err != nil {
				return "", err
			}
			return p.Name, nil
		}})
	a.Training = training.New(training.Config{Ent: a.Ent, Events: c.Events, Changed: a.configChanged})
	a.Watcher = routers.NewWatcher(routers.WatchConfig{Ent: a.Ent, Events: c.Events, Notifier: c.Notifier, Webhooks: c.Webhooks,
		Activity: a.Activity, AppURL: c.Config.AppURL, Changed: a.configChanged})
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
		Ent: a.Ent, Spend: a.Spend, Notifier: c.Notifier, Webhooks: c.Webhooks, Activity: a.Activity, AppURL: c.Config.AppURL,
		Changed: a.configChanged,
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
	return snapshot.Sources{Providers: a.Providers, Keys: a.Keys, Routes: a.Routes, Budgets: a.Budgets, Prices: a.PriceOverrides,
		Rules: a.Rules, Routers: a.Routers, Firewall: a.Firewall, Payloads: a.Payloads}
}
