// Package routers maps Promptdam's own paths to handlers.
package routers

import (
	"net/http"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel/pkg/identity"
	"github.com/hasan-muzak/keel/pkg/tenancy"

	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
	"github.com/hasan-muzak/keel/products/promptdam/server/core"
	"github.com/hasan-muzak/keel/products/promptdam/server/handlers"
)

// Mount registers Promptdam's routes on the API application.
func Mount(a *muzak.App, app *core.App) {
	c := app.Core
	h := &handlers.Handlers{Tenancy: c.Tenancy, Providers: app.Providers}
	r := muzak.NewRouter(
		muzak.WithTags(brand.Key),
		muzak.Needs(identity.Authenticate(c.Verifier)),
		muzak.Needs(c.Tenancy.CurrentUser),
	)
	project := func(perms ...tenancy.Permission) muzak.SharedOption {
		return muzak.Needs(c.Tenancy.RequireProject(perms...))
	}

	prov := muzak.WithCategory("Providers")
	r.Get("/v1/provider-kinds", h.ProviderKinds, prov, muzak.Title("List Provider Kinds"),
		muzak.Summary("The providers a project can add"),
		muzak.Description("Each kind's API format and default base URL. A custom provider is any endpoint speaking the "+
			"OpenAI or the Anthropic API."))
	pb := "/v1/orgs/{org}/projects/{project}/providers"
	r.Get(pb, h.ListProviders, prov, muzak.Title("List Providers"), project(tenancy.PermProjectsRead),
		muzak.Summary("List a project's LLM providers"),
		muzak.Description("The providers the gateway calls with the project's own keys, by priority. Keys are never "+
			"returned, only whether one is set and its last characters."))
	r.Post(pb, h.CreateProvider, prov, muzak.Title("Create Provider"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusCreated), muzak.Summary("Add an LLM provider with the project's own key"),
		muzak.Description("The key is sealed at rest and sent only to the provider's base URL. Custom base URLs must be "+
			"public https addresses. The gateway uses the provider within seconds."))
	r.Get(pb+"/{provider}", h.GetProvider, prov, muzak.Title("Get Provider"), project(tenancy.PermProjectsRead),
		muzak.Summary("Get a provider"))
	r.Put(pb+"/{provider}", h.UpdateProvider, prov, muzak.Title("Update Provider"), project(tenancy.PermProjectsManage),
		muzak.Summary("Replace a provider's fields"),
		muzak.Description("Omit api_key to keep the stored key. A stored key goes only to the endpoint it was given for: "+
			"changing the base URL or kind needs the key again (api_key_required)."))
	r.Delete(pb+"/{provider}", h.DeleteProvider, prov, muzak.Title("Delete Provider"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusNoContent), muzak.Summary("Remove a provider and its key"))
	r.Post(pb+"/{provider}/check", h.CheckProvider, prov, muzak.Title("Check Provider"), project(tenancy.PermProjectsManage),
		muzak.Summary("Check a provider's key against the provider"),
		muzak.Description("Lists the provider's models with the key, which costs nothing, and records whether the "+
			"provider accepted it (checked_at, check_error)."))
	a.Include(r)
}
