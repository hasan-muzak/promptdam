// Package routers maps Promptdam's own paths to handlers.
package routers

import (
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel/pkg/identity"

	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
	"github.com/hasan-muzak/keel/products/promptdam/server/core"
)

// Mount registers Promptdam's routes on the API application.
func Mount(a *muzak.App, app *core.App) {
	c := app.Core
	r := muzak.NewRouter(
		muzak.WithTags(brand.Key),
		muzak.Needs(identity.Authenticate(c.Verifier)),
		muzak.Needs(c.Tenancy.CurrentUser),
	)
	a.Include(r)
}
