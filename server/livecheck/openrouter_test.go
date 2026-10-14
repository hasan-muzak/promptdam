package livecheck_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

// stack is what the traffic runs on: the gateway, its prices, the
// catalog that prices the models that answer, the model the app asks for
// (the router "advisor", or a model the gateway passes on as it is), and
// the model "without Promptdam" is priced at: the router's strongest
// tier, every call on it without caching.
type stack struct {
	label    string
	key      string
	w        *gatewaytest.World
	list     *llm.Catalog
	catalog  string
	model    string
	baseline string
}

// The OpenRouter stack's tiers, each a different vendor, cheapest first.
const (
	orFast     = "deepseek/deepseek-v4.1-flash"
	orMid      = "google/gemini-3.8-flash"
	orExpert   = "openai/gpt-6.1-sol"
	orDecision = "openai/gpt-6-luna-decisions"
)

// trafficStack is the stack PROMPTDAM_DEMO_STACK names: "claude" (the
// default: Anthropic's key, Haiku 5.5 and Sonnet 5.5) or "openrouter"
// (OpenRouter's key, PROMPTDAM_TEST_OPENROUTER_KEY: DeepSeek, Gemini and
// GPT tiers, the decision model PROMPTDAM_DEMO_DECIDER, a decision model
// by default). PROMPTDAM_DEMO_MODEL has the app ask for another model than
// the router, such as typesafe/jev-router: then the gateway only passes
// the calls on, and logs and prices them. Without the stack's key, both
// run on the fake provider.
func trafficStack(t *testing.T) stack {
	t.Helper()
	if os.Getenv("PROMPTDAM_DEMO_STACK") != "openrouter" {
		key := os.Getenv("PROMPTDAM_TEST_ANTHROPIC_KEY")
		w, list := pilotGateway(t, key)
		return stack{label: "Claude: Haiku 5.5, Sonnet 5.5, Haiku 5.5 deciding", key: key, w: w, list: list, catalog: "anthropic",
			model: "advisor", baseline: "claude-sonnet-5-5"}
	}
	key := os.Getenv("PROMPTDAM_TEST_OPENROUTER_KEY")
	decider := os.Getenv("PROMPTDAM_DEMO_DECIDER")
	if decider == "" {
		decider = orDecision
	}
	model := os.Getenv("PROMPTDAM_DEMO_MODEL")
	if model == "" || key == "" {
		model = "advisor"
	}
	if key == "" {
		w, list := pilotGateway(t, "")
		return stack{label: "OpenRouter (fake provider)", w: w, list: list, catalog: "openrouter", model: model, baseline: orExpert}
	}
	w, list := openRouterGateway(t, key, decider)
	label := "OpenRouter: " + orFast + ", " + orMid + ", " + orExpert + "; " + decider + " deciding"
	if model != "advisor" {
		label = "OpenRouter: " + model + " (no Promptdam router)"
	}
	return stack{label: label, key: key, w: w, list: list, catalog: "openrouter", model: model, baseline: orExpert}
}

// openRouterGateway is CustomsPilot's project on OpenRouter: router
// "advisor" with a tier per vendor and decider deciding (a decision model,
// or a chat model), prompt caching on, prices synced from OpenRouter.
func openRouterGateway(t *testing.T, key, decider string) (*gatewaytest.World, *llm.Catalog) {
	t.Helper()
	ctx := context.Background()
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	w.Grant(catalog.Routers, catalog.RoutingLearned, catalog.PromptCaching)
	if err := w.App.Prices.Sync(ctx, true); err != nil {
		t.Fatalf("syncing prices: %v", err)
	}
	for _, m := range []string{orFast, orMid, orExpert, decider} {
		p, ok := w.App.Prices.Current().Lookup("openrouter", m)
		if !ok {
			t.Fatalf("no price for %s", m)
		}
		t.Logf("price %s: input $%g, cache read $%g, output $%g per million, decision model %v", m, p.Input, p.CachedInput, p.Output, p.Decisions)
	}
	if _, err := w.App.Providers.Create(w.Scoped(), w.Org, w.Project, providers.Input{Name: "openrouter", Kind: "openrouter",
		APIKey: &key, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "advisor", Status: tiers.Active,
		DecisionModel: "openrouter/" + decider,
		Spec: tiers.Spec{Tiers: []tiers.Tier{
			{Name: "fast", Model: "openrouter/" + orFast,
				Description: "Definitions and single facts, rewording, translations, short emails, summaries and to-do lists of what the conversation already covered.",
				Examples:    []string{"How many partners can a limited company have at most?", "Can you sum this up in three points?"}},
			{Name: "mid", Model: "openrouter/" + orMid,
				Description: "Explanations and comparisons that need some reasoning but no calculation or legal judgment: procedures, document lists, choosing between options.",
				Examples:    []string{"What's the difference between a bill of lading and a CMR?"}},
			{Name: "expert", Model: "openrouter/" + orExpert,
				Description: "Legal analysis and comparisons across laws, tax and liability trade-offs, customs value and tax calculations, tariff classification with reasoning, checking documents against each other.",
				Examples:    []string{"Compare a sole proprietorship and a limited company on tax and liability."}},
		}, Default: "expert"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	return w, w.App.Prices.Current()
}

// served is the model a call's answer came from, as the price list names
// it: OpenRouter answers with dated names (deepseek/deepseek-v4.1-flash-20260910).
func (s stack) price(model string) (llm.Model, bool) {
	if model == "" {
		return llm.Model{}, false
	}
	return s.list.Lookup(s.catalog, strings.TrimSpace(model))
}
