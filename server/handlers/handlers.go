// Package handlers are Promptdam's own API handlers, next to the shared
// platform routes.
package handlers

import (
	"log/slog"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/audit"
	"github.com/hasan-muzak/keel-core/pkg/ent"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/notify"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/activity"
	"github.com/hasan-muzak/promptdam/server/features/alerts"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/cachedoctor"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/promptdam/server/features/payloads"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/privacy"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/responsecache"
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
	Alerts     *alerts.Service
	Payloads   *payloads.Service
	// PayloadStore reads and deletes kept payloads (nil without object
	// storage: none are kept).
	PayloadStore *payloads.Store
	// Privacy keeps organizations' payload-free mode.
	Privacy *privacy.Service
	// Cache is the response cache, cleared when a route changes.
	Cache *responsecache.Cache
	// Audit is the core graph, where changes are audited.
	Audit *ent.Client
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

// audited records a change to the project in the organization's audit
// log, with who made it and from where. The change is made: an event that
// can't be written is logged, not returned.
func (h *Handlers) audited(ctx *muzak.Context, action, targetType, targetID string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["project"] = ids.Encode(ids.Project, pa.Project.ID)
	err := audit.Record(audit.Request(ctx, pa.Actor()), h.Audit, audit.Event{OrgID: pa.OrgID(), Action: action,
		TargetType: targetType, TargetID: targetID, Meta: meta})
	if err != nil {
		slog.Warn("handlers: a change wasn't audited", "action", action, "target", targetID, "err", err)
	}
}
