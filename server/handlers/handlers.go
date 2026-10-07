// Package handlers are Promptdam's own API handlers, next to the shared
// platform routes.
package handlers

import (
	"github.com/hasan-muzak/keel/pkg/notify"
	"github.com/hasan-muzak/keel/pkg/tenancy"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/analytics"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/budgets"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/routes"
)

// Handlers hold Promptdam's services.
type Handlers struct {
	Tenancy   *tenancy.Service
	Providers *providers.Service
	Keys      *keysettings.Service
	Routes    *routes.Service
	Budgets   *budgets.Service
	Notifier  *notify.Notifier
	Analytics *analytics.Service
}
