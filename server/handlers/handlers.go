// Package handlers are Promptdam's own API handlers, next to the shared
// platform routes.
package handlers

import (
	"github.com/hasan-muzak/keel-core/pkg/notify"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/cachedoctor"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
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
}
