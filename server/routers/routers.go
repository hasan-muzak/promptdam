// Package routers maps Promptdam's own paths to handlers.
package routers

import (
	"net/http"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/identity"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/core"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/cachedoctor"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/responsecache"
	"github.com/hasan-muzak/promptdam/server/handlers"
)

// Mount registers Promptdam's routes on the API application.
func Mount(a *muzak.App, app *core.App) {
	c := app.Core
	h := &handlers.Handlers{
		Tenancy: c.Tenancy, Providers: app.Providers, Keys: app.Keys, Routes: app.Routes, Budgets: app.Budgets, Notifier: c.Notifier,
		Analytics: analytics.New(c.Events), Prices: app.PriceOverrides, Families: families.NewService(app.Ent, c.Events),
		CacheDoctor: cachedoctor.New(c.Events, app.Prices, app.CachePolicy, app.CacheProfiles), Rules: app.Rules,
		RuleDeps: handlers.RuleDeps{Env: app.RuleEnv, Events: c.Events, Prices: app.Prices},
		Routers:  app.Routers, Firewall: app.Firewall, Activity: app.Activity, Alerts: app.Alerts, Payloads: app.Payloads, PayloadStore: app.PayloadStore, Audit: c.DB.Ent,
		Cache: responsecache.New(c.KV), Privacy: app.Privacy,
		RouterDeps: handlers.RouterDeps{Explainer: resolver.New(resolver.Config{Prices: app.Prices, Rules: app.RuleEnv}),
			Sources: app.Sources(), Billing: c.Billing, AppURL: c.Config.AppURL, Events: c.Events, Prices: app.Prices, Jobs: c.Jobs},
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

	r.Get("/v1/orgs/{org}/projects/{project}/models", h.ListModels, prov, muzak.Title("List Models"),
		project(tenancy.PermProjectsRead), muzak.Summary("The models the project's providers serve, for model pickers"),
		muzak.Description("Each model with the providers serving it (those of api_format first, then by priority), what it "+
			"takes (tools, images, files, JSON schema, reasoning; null when the price list doesn't say), its context and "+
			"answer limits, the price list's price and the project's own. Providers that take any model name (a custom "+
			"endpoint without a list, OpenRouter) are listed apart: type the model."))

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
	routing := muzak.Needs(platform.RequireFeature(c.Billing, c.Config.AppURL, catalog.RoutingRules))
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

	acat := muzak.WithCategory("Alert Rules")
	ab := "/v1/orgs/{org}/projects/{project}/alerts"
	r.Get(ab, h.ListAlerts, acat, muzak.Title("List Alert Rules"), project(tenancy.PermProjectsRead),
		muzak.Summary("A project's alert rules and where each stands"),
		muzak.Description("Rules watch the project's calls every minute and alert its channels when they go wrong: spend "+
			"spiking above its usual hour, provider errors over a window, a provider that looks down, slow answers. Budgets "+
			"cap spend; alert rules say something is wrong first."))
	r.Post(ab, h.CreateAlert, acat, muzak.Title("Create Alert Rule"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusCreated), muzak.Summary("Add an alert rule"),
		muzak.Description("A rule fires once when its condition starts to hold (alert.fired), again after its cooldown while "+
			"it still holds, and resolves when it no longer does (alert.resolved). Each goes to its channels, the "+
			"organization's webhooks and the project's activity. Needs a plan with alert rules; fewer calls than "+
			"min_calls in the window never fire it."))
	r.Get(ab+"/{alert}", h.GetAlert, acat, muzak.Title("Get Alert Rule"), project(tenancy.PermProjectsRead),
		muzak.Summary("Get an alert rule"))
	r.Put(ab+"/{alert}", h.UpdateAlert, acat, muzak.Title("Update Alert Rule"), project(tenancy.PermProjectsManage),
		muzak.Summary("Replace an alert rule"), muzak.Description("A changed condition starts the rule anew, not firing."))
	r.Delete(ab+"/{alert}", h.DeleteAlert, acat, muzak.Title("Delete Alert Rule"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusNoContent), muzak.Summary("Remove an alert rule"))

	log := muzak.WithCategory("Request Log")
	pl := "/v1/orgs/{org}/projects/{project}/payloads"
	r.Get(pl+"/settings", h.GetCapture, log, muzak.Title("Get Payload Capture"), project(tenancy.PermProjectsRead),
		muzak.Summary("Whether the project keeps its calls' prompts and answers"),
		muzak.Description("Off by default: the request log keeps metadata only. On, the gateway keeps each call's request as "+
			"the provider got it and the answer the client got, with secrets, personal data and the project's own "+
			"patterns always masked, sealed and kept as long as the plan keeps logs."))
	r.Put(pl+"/settings", h.PutCapture, log, muzak.Title("Set Payload Capture"), project(tenancy.PermProjectsManage),
		muzak.Summary("Turn the project's payload capture on or off"),
		muzak.Description("Every plan. sample_rate keeps a share of calls; answers false keeps requests only. Gateways apply "+
			"it within seconds. Payloads already kept stay until they expire or are deleted."))
	lb := "/v1/orgs/{org}/projects/{project}/requests"
	r.Get(lb, h.ListRequests, log, muzak.Title("List Requests"), project(tenancy.PermProjectsRead),
		muzak.Summary("List a project's gateway calls, newest first"),
		muzak.Description("Metadata only, never prompts or answers: models, provider, status, tokens, cost, timings. "+
			"Covers at most 31 days per query (default the last 24 hours) and what the plan keeps."))
	r.Get(lb+"/{request}/payload", h.GetPayload, log, muzak.Title("Get Request Payload"), project(tenancy.PermProjectsManage),
		muzak.Summary("What was kept of a call: its request and answer, masked"),
		muzak.Description("For projects that keep payloads (…/payloads/settings): the request as the provider got it and "+
			"the answer the client got (its text and tool calls; a failed call's error), with secrets, personal data and "+
			"the project's own patterns masked. Developers and up; every read is audited. 404 payload_gone once the "+
			"plan's retention ended or the payloads were deleted."))
	r.Delete(pl, h.DeletePayloads, log, muzak.Title("Delete Payloads"), project(tenancy.PermProjectsManage),
		muzak.Summary("Delete every payload the project kept, now"),
		muzak.Description("Payloads a gateway is still gathering are written within a minute, then kept as their plan says: "+
			"turn capture off first to keep none. Audited."))
	r.Get(lb+"/{request}", h.GetRequest, log, muzak.Title("Get Request"), project(tenancy.PermProjectsRead),
		muzak.Summary("Get one gateway call by its trace (X-Promptdam-Trace)"))

	rucat := muzak.WithCategory("Routing Rules")
	rb2 := "/v1/orgs/{org}/projects/{project}/rules"
	r.Get(rb2, h.ListRules, rucat, muzak.Title("List Rules"), project(tenancy.PermProjectsRead),
		muzak.Summary("A project's routing rules, in evaluation order"))
	r.Post(rb2, h.CreateRule, rucat, muzak.Title("Create Rule"), project(tenancy.PermProjectsManage), routing,
		muzak.Status(http.StatusCreated), muzak.Summary("Add a routing rule"),
		muzak.Description("A rule sends the calls its CEL condition holds for to another model (same API format). "+
			"Rules start as drafts; try them with a dry-run, then make them active. Compile errors come back "+
			"with their line and column."))
	r.Post(rb2+"/dry-run", h.DryRunRule, rucat, muzak.Title("Dry-Run Rule"), project(tenancy.PermProjectsRead),
		muzak.Summary("Try a rule on recent calls before saving it"),
		muzak.Description("Reads the range's most recent 20,000 calls (default the last 7 days) and tells how many the "+
			"rule would have routed and what they would have cost on its model. Cost only: quality needs trying the "+
			"model on them."))
	r.Get(rb2+"/{rule}", h.GetRule, rucat, muzak.Title("Get Rule"), project(tenancy.PermProjectsRead),
		muzak.Summary("Get a routing rule"))
	r.Put(rb2+"/{rule}", h.UpdateRule, rucat, muzak.Title("Update Rule"), project(tenancy.PermProjectsManage), routing,
		muzak.Summary("Replace a routing rule"), muzak.Description("Activate it with status active; the gateway applies it within seconds."))
	r.Delete(rb2+"/{rule}", h.DeleteRule, rucat, muzak.Title("Delete Rule"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusNoContent), muzak.Summary("Remove a routing rule"))
	r.Post(rb2+"/{rule}/dry-run", h.DryRunSavedRule, rucat, muzak.Title("Dry-Run Saved Rule"),
		project(tenancy.PermProjectsRead), muzak.Summary("Try a saved rule on recent calls"))

	mrcat := muzak.WithCategory("Model Routers")
	mr := "/v1/orgs/{org}/projects/{project}/routers"
	r.Get(mr, h.ListRouters, mrcat, muzak.Title("List Routers"), project(tenancy.PermProjectsRead),
		muzak.Summary("A project's model routers"),
		muzak.Description("A router places each call in one of its tiers of models, from cheapest to strongest: the tier "+
			"the client names, else the first rule that holds, else (with evaluation) the classifier learned from the "+
			"project's judged calls or the decision model, else the default tier. Clients call it like a model."))
	r.Post(mr, h.CreateRouter, mrcat, muzak.Title("Create Router"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusCreated), muzak.Summary("Add a model router"),
		muzak.Description("Routers start in shadow mode: they decide every call and log their choice, but serve the default "+
			"tier, until made active (a plan with model routers). Tiers may name models of either API format: calls are "+
			"translated. Rule compile errors come back with their line and column."))
	r.Get(mr+"/suggest", h.SuggestRouter, mrcat, muzak.Title("Suggest Router"), project(tenancy.PermProjectsRead),
		muzak.Summary("A router to start from, built from the project's providers' models"),
		muzak.Description("Tiers from cheapest to strongest, each the newest model of a known family (Claude Haiku, Sonnet "+
			"and Opus; GPT nano, mini and the base model; Gemini Flash-Lite, Flash and Pro; Mistral, DeepSeek and Grok "+
			"likewise) a provider of the project serves, else picked by price, with the same tier of the other providers "+
			"as fallbacks. The model the project used most in the last 7 days (or current_model) becomes the default "+
			"tier. Create it as is, in shadow mode, or edit it first."))
	r.Get(mr+"/{router}", h.GetRouter, mrcat, muzak.Title("Get Router"), project(tenancy.PermProjectsRead),
		muzak.Summary("Get a model router"))
	r.Put(mr+"/{router}", h.UpdateRouter, mrcat, muzak.Title("Update Router"), project(tenancy.PermProjectsManage),
		muzak.Summary("Replace a model router"), muzak.Description("Gateways apply the change within seconds."))
	r.Delete(mr+"/{router}", h.DeleteRouter, mrcat, muzak.Title("Delete Router"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusNoContent), muzak.Summary("Remove a model router"),
		muzak.Description("Refused (409 router_in_use) while a route sends its calls through it."))
	r.Get(mr+"/{router}/report", h.GetRouterReport, mrcat, muzak.Title("Get Router Report"), project(tenancy.PermProjectsRead),
		muzak.Summary("How a model router did: where its calls went, what its choices are worth, each tier's measured quality"),
		muzak.Description("Calls per tier served and chosen, and by the step that chose them; their cost, what they would "+
			"have cost on the chosen tiers (in shadow mode, what following the router would save, also over 30 days); "+
			"each tier's judged calls against the default tier with a 95% interval and whether it meets the quality "+
			"floor; the classifier's held-out precision and coverage per tier; the families judged most. Default the "+
			"last 7 days; at most 90."))
	r.Post(mr+"/{router}/restore", h.RestoreTier, mrcat, muzak.Title("Restore Tier"), project(tenancy.PermProjectsManage),
		muzak.Summary("Put a demoted tier back in the router's placements"),
		muzak.Description("The canary demotes an active router's cheaper tier whose answers, on the calls it served over the "+
			"last 7 days, are below the quality floor with 95% confidence (router.tier_demoted). Restore it once its model "+
			"or the router changed."))
	r.Post(mr+"/{router}/train", h.TrainRouter, mrcat, muzak.Title("Train Router"), project(tenancy.PermProjectsManage),
		muzak.Status(http.StatusAccepted), muzak.Summary("Train the router's classifier now"),
		muzak.Description("Routers with new judged calls are trained hourly; this queues a training at once. Gateways use "+
			"the new classifier within seconds of it."))
	r.Post(mr+"/{router}/explain", h.ExplainRouter, mrcat, muzak.Title("Explain Router"), project(tenancy.PermProjectsRead),
		muzak.Summary("How the router would place a request, without calling any provider"),
		muzak.Description("The tier it would serve and why (the step that chose it, the rule that held), what the request "+
			"needs of a model (tools, images, files, a JSON schema, context), which tiers' models can take it, and the "+
			"providers and models the gateway would try, in order."))

	fwcat := muzak.WithCategory("Firewall")
	r.Get("/v1/firewall/presets", h.FirewallPresets, fwcat, muzak.Title("List Firewall Presets"),
		muzak.Summary("What the firewall looks for: each class's detectors"),
		muzak.Description("The source code preset is the secret class's detectors, the customer data preset the personal "+
			"data's. Detector names are what pii_detectors, events and policies' findings use; a project's own patterns "+
			"are the custom class."))
	fw := "/v1/orgs/{org}/projects/{project}/firewall"
	r.Get(fw, h.GetFirewall, fwcat, muzak.Title("Get Firewall"), project(tenancy.PermProjectsRead),
		muzak.Summary("What the firewall does with the secrets and personal data a project's calls carry"),
		muzak.Description("Every text of a request is scanned before anything is sent. Each finding's class says what "+
			"happens: secrets (API keys, tokens, private keys, connection strings), personal data and the project's own "+
			"patterns are blocked, redacted (masked), tokenized (the model sees a placeholder, the answer gets the value "+
			"back), alerted on, or let be. A project that set nothing blocks secrets and scans answers."))
	r.Put(fw, h.PutFirewall, fwcat, muzak.Title("Set Firewall"), project(tenancy.PermProjectsManage),
		muzak.Summary("Replace a project's firewall"),
		muzak.Description("Redacting or tokenizing personal data needs a plan with PII redaction, policies one with "+
			"firewall policies, custom patterns one with custom policies. A downgrade never loosens a firewall: what is "+
			"saved keeps applying, and may be kept or removed. Gateways apply a change within seconds. Policy compile "+
			"errors come back with their line and column."))
	r.Get(fw+"/events", h.ListFirewallEvents, fwcat, muzak.Title("List Firewall Events"), project(tenancy.PermProjectsRead),
		muzak.Summary("What the firewall found in a project's calls, newest first"),
		muzak.Description("One event per detector and part of a call it found something in (answers included), or the "+
			"policy that decided a call: what it got, how many, and whether the call was refused. Never the values. "+
			"Covers at most 31 days per query (default the last 7) and what the plan keeps."))
	r.Get(fw+"/stats", h.GetFirewallStats, fwcat, muzak.Title("Get Firewall Stats"), project(tenancy.PermProjectsRead),
		muzak.Summary("What the firewall did over a time range"),
		muzak.Description("Calls it found something in and refused, findings per action and detector, and a daily series. "+
			"Default the last 7 days; at most 90."))
	r.Post(fw+"/test", h.TestFirewall, fwcat, muzak.Title("Test Firewall"), project(tenancy.PermProjectsRead),
		muzak.Summary("What the firewall would do with a request, without calling any provider"),
		muzak.Description("What it finds where, what each finding gets, the request as the provider would get it, and "+
			"the policy that decides. Try settings before saving them with settings."))

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
	r.Get(pr+"/missing", h.MissingPrices, pcat, muzak.Title("List Missing Prices"), project(tenancy.PermProjectsRead),
		muzak.Summary("Models the project's calls were served by without a price"),
		muzak.Description("Their spend and savings are unknown: set the project's own price, or wait for the price list "+
			"to price them. The busiest first; default the last 7 days, at most 31."))
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
	r.Get(fb+"/{family}/cache-doctor", h.GetFamilyCacheDoctor, fcat, muzak.Title("Diagnose Family Caching"),
		project(tenancy.PermProjectsRead), muzak.Summary("Why a family's calls miss the providers' prompt caches, and the fix"))
	r.Get("/v1/orgs/{org}/projects/{project}/cache-doctor", h.GetCacheDoctor, fcat, muzak.Title("Diagnose Caching"),
		project(tenancy.PermProjectsRead), muzak.Summary("The 20 busiest families' prompt cache health, by what fixing it is worth"),
		muzak.Description("Read from the request log's prefix rollup, never from prompts: how often a family's prefix "+
			"changes and why (dates, IDs, numbers, tool order), missing Anthropic breakpoints, prefixes too short to "+
			"cache. Default the last 7 days."))

	r.Get("/v1/orgs/{org}/projects/{project}/activity", h.ListActivity, muzak.WithCategory("Activity"), muzak.Title("List Activity"),
		project(tenancy.PermProjectsRead), muzak.Summary("What happened to the project that was worth telling, newest first"),
		muzak.Description("Budget thresholds crossed, routers ready to serve or tiers demoted, calls the firewall refused, alert "+
			"rules firing and resolving: what the organization's channels and webhooks got as it happened. A budget's, router's "+
			"or alert rule's own history with subject. Kept 90 days."))

	priv := muzak.WithCategory("Privacy")
	r.Get("/v1/orgs/{org}/privacy", h.GetPrivacy, priv, muzak.Title("Get Privacy"),
		muzak.Needs(c.Tenancy.RequireOrg(tenancy.PermOrgRead)),
		muzak.Summary("Whether the organization is payload-free"))
	r.Put("/v1/orgs/{org}/privacy", h.PutPrivacy, priv, muzak.Title("Set Privacy"),
		muzak.Needs(c.Tenancy.RequireOrg(tenancy.PermOrgUpdate)),
		muzak.Summary("Turn payload-free mode on or off"),
		muzak.Description("Payload-free mode (Business) keeps any prompt or answer from being stored in any of the "+
			"organization's projects: payload capture, firewall excerpts and the response cache are off, and turning "+
			"them on is refused (409 payload_free). Turning it on also deletes, at once, every payload and cached "+
			"answer the projects kept. A downgrade keeps it on; turning it off needs no plan. Audited."))

	ana := muzak.WithCategory("Analytics")
	r.Get("/v1/orgs/{org}/projects/{project}/overview", h.GetOverview, ana, muzak.Title("Get Overview"),
		project(tenancy.PermProjectsRead), muzak.Summary("A project at a glance, in one call"),
		muzak.Description("Calls, errors and spend against the same length of time before, what they would have cost "+
			"without Promptdam and what was saved, a daily series of both, p95 latency, prompt and response cache use, the "+
			"firewall's calls and refusals, the top models, keys and families, what needs attention (budgets past a "+
			"threshold, routers ready, demoted or kept watching by the plan, providers failing their check, alert rules "+
			"firing, models without a price), most urgent first, and the latest activity. Default this month."))
	r.Get("/v1/orgs/{org}/analytics/projects", h.GetProjectsUsage, ana, muzak.Title("Get Projects Usage"),
		muzak.Needs(c.Tenancy.RequireOrg(tenancy.PermProjectsRead)),
		muzak.Summary("Each project's calls, spend and savings over a range"),
		muzak.Description("Every project of the organization, by spend, those without calls last. Default this month; at "+
			"most 400 days."))
	r.Get("/v1/orgs/{org}/projects/{project}/analytics/spend", h.GetSpend, ana, muzak.Title("Get Spend"),
		project(tenancy.PermProjectsRead), muzak.Summary("Calls, errors, tokens and spend over a range"),
		muzak.Description("Totals, a series per hour or day, and groups by model, provider, key, route, developer or "+
			"tag (at most 100, by spend). Default the last 7 days; at most 400 days."))
	r.Get("/v1/orgs/{org}/projects/{project}/analytics/savings", h.GetSavings, ana, muzak.Title("Get Savings"),
		project(tenancy.PermProjectsRead), muzak.Summary("The savings ledger: what calls cost against the model asked for"),
		muzak.Description("Each call's counterfactual is the model asked for at list price without any cache; savings "+
			"are the difference, by reason (response_cache, routing, fallback, prompt_cache) and family. Default the "+
			"last 30 days."))
	r.Get("/v1/orgs/{org}/projects/{project}/analytics/savings/export", h.ExportSavings, ana, muzak.Title("Export Savings Ledger"),
		project(tenancy.PermProjectsRead), muzak.Summary("The savings ledger as CSV: a line per call"), muzak.Produces("text/csv"),
		muzak.Description("text/csv, newest first:time, request, key, developer, end user, route, family, requested and served "+
			"model, provider, status, error code, tokens, cost, counterfactual (the model asked for at list price, without any "+
			"cache) and what was saved, in USD, with the reason and the response cache's answer. Default the last 30 days; at "+
			"most 31 per export. Needs a plan with the ledger export."))
	r.Get("/v1/orgs/{org}/projects/{project}/analytics/latency", h.GetLatency, ana, muzak.Title("Get Latency"),
		project(tenancy.PermProjectsRead), muzak.Summary("Latency and time-to-first-byte percentiles per model or provider"))
	a.Include(r)
}
