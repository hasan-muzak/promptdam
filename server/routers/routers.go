// Package routers maps Promptdam's own paths to handlers.
package routers

import (
	"net/http"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel/pkg/identity"
	"github.com/hasan-muzak/keel/pkg/platform"
	"github.com/hasan-muzak/keel/pkg/tenancy"

	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
	"github.com/hasan-muzak/keel/products/promptdam/server/catalog"
	"github.com/hasan-muzak/keel/products/promptdam/server/core"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/analytics"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/families"
	"github.com/hasan-muzak/keel/products/promptdam/server/handlers"
)

// Mount registers Promptdam's routes on the API application.
func Mount(a *muzak.App, app *core.App) {
	c := app.Core
	h := &handlers.Handlers{
		Tenancy: c.Tenancy, Providers: app.Providers, Keys: app.Keys, Routes: app.Routes, Budgets: app.Budgets, Notifier: c.Notifier,
		Analytics: analytics.New(c.Events), Prices: app.PriceOverrides, Families: families.NewService(app.Ent, c.Events),
	}
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

	keys := muzak.WithCategory("Gateway Keys")
	kb := "/v1/orgs/{org}/projects/{project}/keys/{key}/gateway"
	r.Get(kb, h.GetKeySettings, keys, muzak.Title("Get Key Settings"), project(tenancy.PermProjectsRead),
		muzak.Summary("Get what the gateway applies to an API key"),
		muzak.Description("A key without settings may call every model, without a rate limit of its own. Keys are "+
			"issued with the gateway scope under the project's keys."))
	r.Put(kb, h.PutKeySettings, keys, muzak.Title("Set Key Settings"), project(tenancy.PermProjectsManage),
		muzak.Summary("Replace what the gateway applies to an API key"),
		muzak.Description("Allowed models, the developer and tags for attribution, a rate limit and a default route. "+
			"The gateway applies them within seconds; a rotated key's replacement keeps them."))

	rcat := muzak.WithCategory("Routes")
	rb := "/v1/orgs/{org}/projects/{project}/routes"
	r.Get(rb, h.ListRoutes, rcat, muzak.Title("List Routes"), project(tenancy.PermProjectsRead),
		muzak.Summary("List a project's routes"),
		muzak.Description("Requests take the route they name in X-Promptdam-Route, else their key's, else the "+
			"route named default. A route says what to try when the model asked for fails."))
	r.Post(rb, h.CreateRoute, rcat, muzak.Title("Create Route"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusCreated), muzak.Summary("Add a route"),
		muzak.Description("Fallbacks are tried in order on rate limits, overload, server errors and timeouts, while "+
			"nothing has reached the client; same API format only (OpenAI or Anthropic)."))
	r.Get(rb+"/{route}", h.GetRoute, rcat, muzak.Title("Get Route"), project(tenancy.PermProjectsRead),
		muzak.Summary("Get a route"))
	r.Put(rb+"/{route}", h.UpdateRoute, rcat, muzak.Title("Update Route"), project(tenancy.PermProjectsManage),
		muzak.Summary("Replace a route"))
	r.Delete(rb+"/{route}", h.DeleteRoute, rcat, muzak.Title("Delete Route"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusNoContent), muzak.Summary("Remove a route"))

	bcat := muzak.WithCategory("Budgets")
	feature := muzak.Needs(platform.RequireFeature(c.Billing, c.Config.AppURL, catalog.Budgets))
	bb := "/v1/orgs/{org}/projects/{project}/budgets"
	r.Get(bb, h.ListBudgets, bcat, muzak.Title("List Budgets"), project(tenancy.PermProjectsRead),
		muzak.Summary("List a project's LLM budgets with their current window"))
	r.Post(bb, h.CreateBudget, bcat, muzak.Title("Create Budget"), project(tenancy.PermProjectsManage), feature,
		muzak.Status(http.StatusCreated), muzak.Summary("Cap what a scope spends per day or month"),
		muzak.Description("A budget caps the project, one API key, one developer or one route. Thresholds alert the "+
			"channels and send budget.threshold_reached (budget.exceeded at 100%) once per window; a blocking budget "+
			"also makes the gateway refuse the scope's calls (429 budget_exceeded) until the window ends."))
	r.Get(bb+"/{budget}", h.GetBudget, bcat, muzak.Title("Get Budget"), project(tenancy.PermProjectsRead),
		muzak.Summary("Get a budget with its current window"))
	r.Put(bb+"/{budget}", h.UpdateBudget, bcat, muzak.Title("Update Budget"), project(tenancy.PermProjectsManage), feature,
		muzak.Summary("Replace a budget"), muzak.Description("A new scope, window or limit starts its alerts again."))
	r.Delete(bb+"/{budget}", h.DeleteBudget, bcat, muzak.Title("Delete Budget"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusNoContent), muzak.Summary("Remove a budget"))

	log := muzak.WithCategory("Request Log")
	lb := "/v1/orgs/{org}/projects/{project}/requests"
	r.Get(lb, h.ListRequests, log, muzak.Title("List Requests"), project(tenancy.PermProjectsRead),
		muzak.Summary("List a project's gateway calls, newest first"),
		muzak.Description("Metadata only, never prompts or answers: models, provider, status, tokens, cost, timings. "+
			"Covers at most 31 days per query (default the last 24 hours) and what the plan keeps."))
	r.Get(lb+"/{request}", h.GetRequest, log, muzak.Title("Get Request"), project(tenancy.PermProjectsRead),
		muzak.Summary("Get one gateway call by its trace (X-Promptdam-Trace)"))

	pcat := muzak.WithCategory("Prices")
	pr := "/v1/orgs/{org}/projects/{project}/prices"
	r.Get(pr, h.ListPrices, pcat, muzak.Title("List Prices"), project(tenancy.PermProjectsRead),
		muzak.Summary("List the project's own prices for models"),
		muzak.Description("Costs use the project's own price for a model when it has one (for one provider first, "+
			"then for any), else the price list synced daily from OpenRouter's public models API. Prices only show "+
			"spend and enforce budgets: providers bill their customers directly."))
	r.Put(pr, h.PutPrice, pcat, muzak.Title("Set Price"), project(tenancy.PermProjectsManage),
		muzak.Summary("Set the project's own price for a model"),
		muzak.Description("For negotiated discounts and models the price list doesn't know (self-hosted ones, a "+
			"host's own model names). USD per million tokens; replaces the project's price for the same model and "+
			"provider. Applies to calls from a few seconds later."))
	r.Delete(pr+"/{price}", h.DeletePrice, pcat, muzak.Title("Delete Price"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusNoContent), muzak.Summary("Remove the project's own price: the price list's applies again"))

	fcat := muzak.WithCategory("Prompt Families")
	fb := "/v1/orgs/{org}/projects/{project}/families"
	r.Get(fb, h.ListFamilies, fcat, muzak.Title("List Families"), project(tenancy.PermProjectsRead),
		muzak.Summary("A project's prompt families, by spend"),
		muzak.Description("A family is one task template: calls whose instructions, tools and answer format match once "+
			"changing values (dates, IDs, numbers) are set aside, or that name it in X-Promptdam-Family. Default the "+
			"last 7 days."))
	r.Get(fb+"/{family}", h.GetFamily, fcat, muzak.Title("Get Family"), project(tenancy.PermProjectsRead),
		muzak.Summary("A family's spend, model mix and daily series"))
	r.Put(fb+"/{family}", h.LabelFamily, fcat, muzak.Title("Label Family"), project(tenancy.PermProjectsManage),
		muzak.Summary("Name a family"))

	ana := muzak.WithCategory("Analytics")
	r.Get("/v1/orgs/{org}/projects/{project}/analytics/spend", h.GetSpend, ana, muzak.Title("Get Spend"),
		project(tenancy.PermProjectsRead), muzak.Summary("Calls, errors, tokens and spend over a range"),
		muzak.Description("Totals, a series per hour or day, and groups by model, provider, key, route, developer or "+
			"tag (at most 100, by spend). Default the last 7 days; at most 400 days."))
	r.Get("/v1/orgs/{org}/projects/{project}/analytics/savings", h.GetSavings, ana, muzak.Title("Get Savings"),
		project(tenancy.PermProjectsRead), muzak.Summary("The savings ledger: what calls cost against the model asked for"),
		muzak.Description("Each call's counterfactual is the model asked for at list price without any cache; savings "+
			"are the difference, by reason (response_cache, routing, fallback, prompt_cache) and family. Default the "+
			"last 30 days."))
	r.Get("/v1/orgs/{org}/projects/{project}/analytics/latency", h.GetLatency, ana, muzak.Title("Get Latency"),
		project(tenancy.PermProjectsRead), muzak.Summary("Latency and time-to-first-byte percentiles per model or provider"))
	a.Include(r)
}
