// Package handlers are Promptdam's own API handlers, next to the shared
// platform routes.
package handlers

import (
	"github.com/hasan-muzak/keel/pkg/tenancy"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
)

// Handlers hold Promptdam's services.
type Handlers struct {
	Tenancy   *tenancy.Service
	Providers *providers.Service
}
