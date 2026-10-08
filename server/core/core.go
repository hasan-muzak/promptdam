// Package core describes Promptdam to keel and loads its configuration.
package core

import (
	"github.com/hasan-muzak/keel-core/pkg/apikeys"
	"github.com/hasan-muzak/keel-core/pkg/config"
	"github.com/hasan-muzak/keel-core/pkg/db"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/keel"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/webhooks"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/alerts"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/migrations"
)

// Version is set at build time (-ldflags "-X …/core.Version=v1.2.3").
var Version = "dev"

// Config is Promptdam's environment: keel's plus Promptdam's own settings.
type Config struct {
	keel.Config
	// GatewayAddr is where the gateway data plane listens.
	GatewayAddr string `env:"PROMPTDAM_GATEWAY_ADDR" default:":8282"`
	// GatewayURL is where clients reach the gateway from outside (their
	// base URL is GatewayURL + "/v1"), for setup instructions.
	GatewayURL string `env:"PROMPTDAM_GATEWAY_URL" default:"http://localhost:8282"`
	// ModelPrices is a JSON model price catalog (pkg/llm's shape). Empty:
	// the embedded one.
	ModelPrices string `env:"PROMPTDAM_MODEL_PRICES"`
}

// Load reads the configuration from the environment (and .env).
func Load() (Config, error) { return config.Load[Config]() }

// Prices is the model price catalog the configuration names.
func (c Config) Prices() (*llm.Catalog, error) {
	if c.ModelPrices == "" {
		return llm.Default(), nil
	}
	return llm.Load(c.ModelPrices)
}

// API key scopes. Gateway keys are secret: they call LLM providers with the
// project's own provider keys.
const (
	ScopeGateway = "gateway"
)

// Product is Promptdam as keel sees it.
var Product = keel.Product{
	Name:      brand.Name,
	Key:       brand.Key,
	KeyPrefix: brand.KeyPrefix,
	Description: "An OpenAI- and Anthropic-compatible LLM gateway that cuts the bill with quality-guarded " +
		"routing and caching, and blocks secrets and personal data before they reach a provider.",
	Catalog: catalog.Catalog,
	Scopes: []apikeys.Scope{
		{Name: ScopeGateway, Description: "Call LLM providers through the gateway."},
	},
	Events: []webhooks.EventType{budgets.EventThreshold, budgets.EventExceeded, routers.EventReady,
		routers.EventTierDemoted, firewall.EventBlocked, alerts.EventFired, alerts.EventResolved},
	Migrations:           []db.MigrationSet{migrations.Postgres()},
	ClickHouseMigrations: []events.MigrationSet{migrations.ClickHouse()},
	Color:                "#0b7285",
	SupportEmail:         brand.SupportEmail,
}
