// Package catalog is Promptdam's price list: plans, limits, features, meters
// and overage prices. It is the single source of truth (project.md §5),
// synced to Stripe with `api stripe sync` and exported to the pricing page
// with `api catalog export`.
package catalog

import (
	"github.com/hasan-muzak/keel/pkg/billing"

	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
)

// Meter keys. Promptdam never bills tokens: customers pay their providers
// directly (BYOK). Plans are a subscription plus gateway requests.
const (
	// Requests counts gateway requests: hard on Free, overage on paid plans.
	Requests = "requests"
)

// Limit keys that are not metered.
const (
	Seats = "seats"
	// RetentionDays is how long request logs and firewall events are kept.
	RetentionDays = "retention_days"
	// APIRequestsPerMinute caps an organization's control-plane API calls,
	// all members and keys together (keel's platform.APIRateLimitKey). The
	// gateway has its own per-key limits.
	APIRequestsPerMinute = "api_rpm"
)

// Features.
const (
	// Free: the gateway, fallbacks, the exact response cache, secrets
	// blocking and the (read-only) savings report.
	Gateway         = "gateway"
	Fallbacks       = "gateway.fallbacks"
	ExactCache      = "cache.exact"
	SecretsBlocking = "firewall.secrets"
	SavingsReport   = "savings.report"
	// Pro: automatic prompt caching and the cache doctor, routing rules,
	// budgets and alerts, PII redaction, the ledger export.
	PromptCaching = "cache.prompt"
	RoutingRules  = "routing.rules"
	Budgets       = "budgets"
	PIIRedaction  = "firewall.pii"
	LedgerExport  = "savings.export"
	// Team: savings experiments with auto-revert, CEL firewall policies,
	// per-developer keys, the audit log.
	Experiments      = "experiments"
	FirewallPolicies = "firewall.policies"
	DeveloperKeys    = "keys.developer"
	AuditLog         = "audit"
	// Business: custom policies, payload-free guarantees, DPA, SLA, SSO,
	// priority support.
	CustomPolicies = "firewall.custom"
	PayloadFree    = "payload_free"
	DPA            = "dpa"
	SLA            = "sla"
	SSOSAML        = "sso.saml"
	SupportFast    = "support.priority"
)

var (
	free     = []string{Gateway, Fallbacks, ExactCache, SecretsBlocking, SavingsReport}
	pro      = append(append([]string{}, free...), PromptCaching, RoutingRules, Budgets, PIIRedaction, LedgerExport)
	team     = append(append([]string{}, pro...), Experiments, FirewallPolicies, DeveloperKeys, AuditLog)
	business = append(append([]string{}, team...), CustomPolicies, PayloadFree, DPA, SLA, SSOSAML, SupportFast)
)

// Catalog is Promptdam's price list.
var Catalog = billing.Catalog{
	Product:     brand.Key,
	Currency:    "usd",
	DefaultPlan: "free",
	Meters: []billing.Meter{
		{Key: Requests, Name: "Gateway requests", Unit: "requests", EventName: brand.Key + "_requests"},
	},
	Plans: []billing.Plan{
		{
			Key: "free", Name: "Free", Rank: 0, Public: true,
			Description: "The gateway with fallbacks, caching and secrets blocking.",
			Limits: map[string]int64{
				Requests: 50_000, Seats: 3, RetentionDays: 7, APIRequestsPerMinute: 600,
			},
			Features: free,
		},
		{
			Key: "pro", Name: "Pro", Rank: 1, Public: true, Monthly: 4900, Yearly: 49000,
			Description: "Prompt caching, routing rules, budgets and PII redaction.",
			Limits: map[string]int64{
				Requests: 500_000, Seats: 10, RetentionDays: 30, APIRequestsPerMinute: 2400,
			},
			Features: pro,
			Overage:  map[string]string{Requests: "0.005"}, // $5 per 100k requests
		},
		{
			Key: "team", Name: "Team", Rank: 2, Public: true, Monthly: 19900, Yearly: 199000,
			Description: "Savings experiments, firewall policies and per-developer keys.",
			Limits: map[string]int64{
				Requests: 3_000_000, Seats: billing.Unlimited, RetentionDays: 90, APIRequestsPerMinute: 6000,
			},
			Features: team,
			Overage:  map[string]string{Requests: "0.003"}, // $3 per 100k requests
		},
		{
			Key: "business", Name: "Business", Rank: 3, Public: true, Monthly: 49900, Yearly: 499000,
			Description: "Custom policies, payload-free mode, DPA, SLA and SSO.",
			Limits: map[string]int64{
				Requests: 10_000_000, Seats: billing.Unlimited, RetentionDays: 365, APIRequestsPerMinute: 12000,
			},
			Features: business,
			Overage:  map[string]string{Requests: "0.002"}, // $2 per 100k requests
		},
	},
}
