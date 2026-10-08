// Package handlers are Promptdam's own API handlers, next to the shared
// platform routes.
package handlers

import (
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/notify"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/activity"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/cachedoctor"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
	"github.com/hasan-muzak/promptdam/server/features/rules"
)

// Handlers hold Promptdam's services.
type Handlers struct {
	Tenancy     *tenancy.Service
	Providers   *providers.Service
	Keys        *keysettings.Service
	Routes      *routes.Service
	Budgets     *budgets.Service
	Notifier    *notify.Notifier
	Analytics   *analytics.Service
	Prices      *prices.Overrides
	Families    *families.Service
	CacheDoctor *cachedoctor.Service
	Rules       *rules.Service
	RuleDeps    RuleDeps
	Routers     *routers.Service
	// RouterDeps serve the firewall's routes too: plans, dry-runs.
	RouterDeps RouterDeps
	Firewall   *firewall.Service
	Activity   *activity.Service
}

// requires refuses with 402 plan_required, and where to upgrade, when the
// organization's plan lacks one of the features.
func (h *Handlers) requires(ctx *muzak.Context, features ...string) error {
	if h.RouterDeps.Billing == nil {
		return nil
	}
	_, err := platform.RequireFeature(h.RouterDeps.Billing, h.RouterDeps.AppURL, features...)(ctx)
	return err
}

// planHas reports whether the organization's plan includes a feature; one
// that can't be read hasn't.
func (h *Handlers) planHas(ctx *muzak.Context, feature string) bool {
	if h.RouterDeps.Billing == nil {
		return true
	}
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	e, err := h.RouterDeps.Billing.Entitlements(ctx.Context(), pa.OrgID())
	return err == nil && e.Has(feature)
}
