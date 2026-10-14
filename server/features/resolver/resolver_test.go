package resolver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/apikeys"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/cacheprofile"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/promptdam/server/features/promptcache"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
	"github.com/hasan-muzak/promptdam/server/features/rules"
)

const chat = `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`

func TestCallsAreAuthenticatedAndRouted(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	status, h, body := w.Call("/v1/chat/completions", chat)
	if status != 200 || h.Get(brand.HeaderProvider) != "main" || !strings.Contains(body, "tok0") {
		t.Fatalf("%d %v %s", status, h, body)
	}
	// The provider got the provider's key, never the client's.
	if got := w.Fake.Requests()[0].Header.Get("Authorization"); got != "Bearer sk-main-0000000000" {
		t.Fatalf("the provider got %q", got)
	}
	m, ok := w.Records()[0].Meta.(*resolver.Meta)
	if !ok || m.Project != w.Project || m.Key != w.Key || m.Org != w.Org {
		t.Fatalf("meta %+v", m)
	}

	for _, tt := range []struct {
		header []string
		status int
		code   string
	}{
		{[]string{"Authorization", ""}, 401, "missing_api_key"},
		{[]string{"Authorization", "Bearer pd_sk_live_nope"}, 401, "invalid_api_key"},
		{[]string{brand.HeaderRoute, "nope"}, 400, "unknown_route"},
	} {
		if status, h, body := w.Call("/v1/chat/completions", chat, tt.header...); status != tt.status ||
			h.Get(brand.HeaderErrorCode) != tt.code {
			t.Errorf("%v: %d %s", tt.header, status, body)
		}
	}
	// Anthropic's SDKs send the key in x-api-key; this project has no
	// Anthropic-format provider.
	status, h, _ = w.Call("/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":5,"messages":[]}`,
		"Authorization", "", "X-Api-Key", w.Raw)
	if status != 404 || h.Get(brand.HeaderErrorCode) != "no_provider" {
		t.Fatalf("%d %s", status, h.Get(brand.HeaderErrorCode))
	}

	// A key without the gateway scope can't call.
	_, other, err := w.C.APIKeys.Issue(w.Ctx, apikeys.IssueInput{Org: w.Org, Project: &w.Project, Name: "x", Kind: apikeys.Secret})
	if err != nil {
		t.Fatal(err)
	}
	if status, h, _ := w.Call("/v1/chat/completions", chat, "Authorization", "Bearer "+other); status != 403 ||
		h.Get(brand.HeaderErrorCode) != "key_not_for_gateway" {
		t.Fatalf("%d %s", status, h.Get(brand.HeaderErrorCode))
	}
}

func TestKeySettingsApplyWithinMoments(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	if _, err := w.App.Keys.Put(w.Scoped(), w.Org, w.Project, w.Key, keysettings.Settings{
		AllowedModels: []string{"gpt-5-nano"}, RPM: 3, Developer: "ana",
	}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		status, h, _ := w.Call("/v1/chat/completions", chat)
		return status == 403 && h.Get(brand.HeaderErrorCode) == "model_not_allowed"
	})
	nano := strings.Replace(chat, "gpt-5-mini", "gpt-5-nano", 1)
	codes := map[int]int{}
	for range 4 {
		status, _, _ := w.Call("/v1/chat/completions", nano)
		codes[status]++
	}
	if codes[200] != 3 || codes[429] != 1 {
		t.Fatalf("with 3 requests a minute: %v", codes)
	}
	records := w.Records()
	if m := records[len(records)-2].Meta.(*resolver.Meta); m.Developer != "ana" {
		t.Fatalf("meta %+v", m)
	}
}

func TestBlockingBudget(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	if _, err := w.App.Budgets.Create(w.Scoped(), w.Org, w.Project, budgets.Input{
		Name: "cap", Scope: budgets.ScopeKey, Target: w.Key.String(), Window: "day", LimitMicros: 1000, Action: "block",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := w.Call("/v1/chat/completions", chat); status != 200 {
		t.Fatalf("before spending: %d", status)
	}
	w.App.Spend.Add(budgets.Who{Project: w.Project, Key: w.Key}, 1000)
	w.Eventually(func() bool {
		status, h, _ := w.Call("/v1/chat/completions", chat)
		return status == 429 && h.Get(brand.HeaderErrorCode) == "budget_exceeded" && h.Get("Retry-After") != ""
	})
}

func TestRoutesFallBack(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	w.Provider("backup", providers.FormatOpenAI, []string{"gpt-5-nano"})
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{
		Name: routes.Default, Fallbacks: []string{"backup/gpt-5-nano"}, Retries: 0,
	}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		w.Fake.Fail(http.StatusServiceUnavailable)
		status, h, _ := w.Call("/v1/chat/completions", chat)
		return status == 200 && h.Get(brand.HeaderProvider) == "backup" && h.Get(brand.HeaderServedModel) == "gpt-5-nano"
	})
}

func TestModels(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-haiku-4-5", "claude-sonnet-*"})
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	w.Eventually(func() bool {
		status, _, body := w.Call("/v1/models", "", "Authorization", "", "X-Api-Key", w.Raw, "Anthropic-Version", "2023-06-01")
		return status == 200 && json.Unmarshal([]byte(body), &list) == nil && len(list.Data) == 1 &&
			list.Data[0].ID == "claude-haiku-4-5"
	})
	status, _, body := w.Call("/v1/models", "")
	if status != 200 || !strings.Contains(body, `"object":"list"`) {
		t.Fatalf("%d %s", status, body)
	}
}

// A route with prompt caching sends Anthropic requests with breakpoints.
func TestPromptCaching(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	w.Grant(catalog.PromptCaching)
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-sonnet-5-5"})
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: routes.Default, PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	system, _ := json.Marshal(strings.Repeat("Follow the house style. ", 300))
	body := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":` + string(system) + `,"messages":[{"role":"user","content":"hi"}]}`
	w.Eventually(func() bool {
		status, h, _ := w.Call("/v1/messages", body, "Authorization", "", "X-Api-Key", w.Raw)
		return status == 200 && h.Get(brand.HeaderCache) == "prompt"
	})
	reqs := w.Fake.Requests()
	if sent := string(reqs[len(reqs)-1].Body); !strings.Contains(sent, `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("the provider got no breakpoint: %.200s", sent)
	}
}

// A route keeps prompt caching through a downgrade, but the gateway
// applies it only while the plan includes it.
func TestPromptCachingNeedsThePlan(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-sonnet-5-5"})
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: routes.Default, PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	system, _ := json.Marshal(strings.Repeat("Follow the house style. ", 300))
	body := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":` + string(system) + `,"messages":[{"role":"user","content":"hi"}]}`
	status, h, _ := w.Call("/v1/messages", body, "Authorization", "", "X-Api-Key", w.Raw)
	reqs := w.Fake.Requests()
	if status != 200 || h.Get(brand.HeaderCache) == "prompt" || strings.Contains(string(reqs[len(reqs)-1].Body), "cache_control") {
		t.Fatalf("Free got breakpoints: %d %s", status, h.Get(brand.HeaderCache))
	}
}

// An active rule sends the calls it holds for to its model; drafts don't
// route, and the model asked for stays the first fallback.
func TestRoutingRules(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	w.Provider("small", providers.FormatOpenAI, []string{"gpt-5-nano"})
	for _, in := range []rules.Input{
		{Name: "draft", When: `true`, ToModel: "small/gpt-5-nano", Status: rules.Draft},
		{Name: "short", When: `request.input_tokens < 1500 && !request.has_tools && request.messages == 1`,
			ToModel: "small/gpt-5-nano", Status: rules.Active},
	} {
		if _, err := w.App.Rules.Create(w.Scoped(), w.Org, w.Project, in); err != nil {
			t.Fatal(err)
		}
	}
	w.Eventually(func() bool {
		status, h, _ := w.Call("/v1/chat/completions", chat)
		return status == 200 && h.Get(brand.HeaderProvider) == "small" && h.Get(brand.HeaderServedModel) == "gpt-5-nano"
	})
	recs := w.Records()
	if m := recs[len(recs)-1].Meta.(*resolver.Meta); m.Rule != "short" {
		t.Fatalf("meta %+v", m)
	}

	// A call with tools isn't routed.
	tools := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}]}`
	if status, h, _ := w.Call("/v1/chat/completions", tools); status != 200 || h.Get(brand.HeaderServedModel) != "gpt-5-mini" {
		t.Fatalf("tools: %d %s", status, h.Get(brand.HeaderServedModel))
	}
	recs = w.Records()
	if m := recs[len(recs)-1].Meta.(*resolver.Meta); m.Rule != "" {
		t.Fatalf("tools meta %+v", m)
	}

	// The rule's model failing (a try and a retry), the model asked for
	// answers.
	w.Fake.Fail(http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	if status, h, _ := w.Call("/v1/chat/completions", chat); status != 200 || h.Get(brand.HeaderServedModel) != "gpt-5-mini" {
		t.Fatalf("fallback: %d %s", status, h.Get(brand.HeaderServedModel))
	}
}

// A call for a model only a provider of the other API serves is sent to
// it, translated; so is a fallback naming one.
func TestCallsCrossFormats(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	w.Serves("main", "gpt-5-mini")
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-haiku-4-5"})
	w.Eventually(func() bool {
		status, h, body := w.Call("/v1/chat/completions", `{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}]}`)
		return status == 200 && h.Get(brand.HeaderProvider) == "claude" && strings.Contains(body, `"object":"chat.completion"`)
	})
	reqs := w.Fake.Requests()
	if last := reqs[len(reqs)-1]; last.Path != "/messages" || last.Header.Get("X-Api-Key") != "sk-claude-0000000000" {
		t.Fatalf("upstream %s %v", last.Path, last.Header)
	}

	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{
		Name: routes.Default, Fallbacks: []string{"claude/claude-haiku-4-5"},
	}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		w.Fake.Fail(http.StatusServiceUnavailable)
		status, h, _ := w.Call("/v1/chat/completions", chat)
		return status == 200 && h.Get(brand.HeaderProvider) == "claude" && h.Get(brand.HeaderServedModel) == "claude-haiku-4-5"
	})
	if rec := w.Records()[len(w.Records())-1]; !rec.Translated || !rec.Fallback {
		t.Fatalf("record %+v", rec)
	}
}

// A model router places each call in a tier: the client's, else the first
// rule that holds, else its default; a tier that fails falls back to the
// stronger ones. In shadow mode it decides but serves its default tier.
func TestRouters(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	w.Grant(catalog.Routers)
	w.Serves("main", "gpt-5-nano", "gpt-5-mini")
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-opus-5-5"})
	rt, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Active,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-nano"}, {Name: "smart", Model: "claude-opus-5-5"}},
			Rules: []tiers.Rule{{When: "request.messages == 1 && !request.has_tools", Tier: "fast"}}}})
	if err != nil {
		t.Fatal(err)
	}
	one := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	two := `{"model":"auto","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yes?"},{"role":"user","content":"and?"}]}`

	placed := func(body string, header ...string) (tier, source, served string) {
		t.Helper()
		status, h, out := w.Call("/v1/chat/completions", body, header...)
		if status != 200 || !strings.Contains(out, `"object":"chat.completion"`) {
			t.Fatalf("%d %s", status, out)
		}
		return h.Get(brand.HeaderTier), h.Get(brand.HeaderTierSource), h.Get(brand.HeaderServedModel)
	}
	w.Eventually(func() bool {
		status, h, _ := w.Call("/v1/chat/completions", one)
		return status == 200 && h.Get(brand.HeaderRouter) == "auto"
	})
	if tier, source, served := placed(one); tier != "fast" || source != "rule" || served != "gpt-5-nano" {
		t.Fatalf("a short call: %s %s %s", tier, source, served)
	}
	// No rule holds: the default tier, an Anthropic model, answers the
	// OpenAI-format call.
	if tier, source, served := placed(two); tier != "smart" || source != "default" || served != "claude-opus-5-5" {
		t.Fatalf("a longer call: %s %s %s", tier, source, served)
	}
	if rec := w.Records()[len(w.Records())-1]; !rec.Translated {
		t.Fatalf("record %+v", rec)
	}
	// The client's tier, in the model or a header.
	if tier, source, _ := placed(strings.Replace(one, `"auto"`, `"auto:smart"`, 1)); tier != "smart" || source != "explicit" {
		t.Fatalf("auto:smart: %s %s", tier, source)
	}
	if tier, source, _ := placed(two, brand.HeaderTier, "fast"); tier != "fast" || source != "explicit" {
		t.Fatalf("the header: %s %s", tier, source)
	}
	if status, h, _ := w.Call("/v1/chat/completions", strings.Replace(one, `"auto"`, `"auto:huge"`, 1)); status != 400 ||
		h.Get(brand.HeaderErrorCode) != "unknown_tier" {
		t.Fatalf("an unknown tier: %d %s", status, h.Get(brand.HeaderErrorCode))
	}
	if status, h, _ := w.Call("/v1/embeddings", `{"model":"auto","input":"hi"}`); status != 400 || h.Get(brand.HeaderErrorCode) != "router_format" {
		t.Fatalf("embeddings: %d %s", status, h.Get(brand.HeaderErrorCode))
	}
	// The fast tier failing (a try and a retry), the stronger one answers.
	w.Fake.Fail(http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	if tier, _, served := placed(one); tier != "fast" || served != "claude-opus-5-5" {
		t.Fatalf("fallback: %s %s", tier, served)
	}
	m := w.Records()[len(w.Records())-1].Meta.(*resolver.Meta)
	if m.Router != "auto" || m.Tier != "fast" || m.TierSource != "rule" || m.Baseline.Model != "claude-opus-5-5" {
		t.Fatalf("meta %+v", m)
	}

	// In shadow mode the router decides, and the default tier serves.
	in := routers.Input{Name: "auto", Status: tiers.Shadow, Spec: tiers.Spec{Tiers: rt.Tiers, Rules: rt.Rules}}
	if _, err := w.App.Routers.Update(w.Scoped(), w.Project, rt.ID, in); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		_, h, _ := w.Call("/v1/chat/completions", one)
		return h.Get(brand.HeaderTierSource) == "shadow:rule" && h.Get(brand.HeaderTier) == "smart"
	})
	m = w.Records()[len(w.Records())-1].Meta.(*resolver.Meta)
	if !m.Shadow || m.Decided != "fast" || m.Tier != "smart" {
		t.Fatalf("shadow meta %+v", m)
	}

	// A route sends every call through the router, whatever model it names.
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "agents", Router: "auto", Retries: 1}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		_, h, _ := w.Call("/v1/chat/completions", chat, brand.HeaderRoute, "agents")
		return h.Get(brand.HeaderRouter) == "auto" && h.Get(brand.HeaderTier) == "smart"
	})
	m = w.Records()[len(w.Records())-1].Meta.(*resolver.Meta)
	if m.Baseline.Model != "" {
		t.Fatalf("a call naming its model counts savings against it: %+v", m.Baseline)
	}
	// A router routes send calls through can't be deleted.
	var inUse *routers.InUseError
	if err := w.App.Routers.Delete(w.Scoped(), w.Project, rt.ID); !errors.As(err, &inUse) || inUse.Routes[0] != "agents" {
		t.Fatalf("delete: %v", err)
	}
}

// Tags a client sends are those a key could carry; others are dropped.
func TestHeaderTags(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	if status, _, body := w.Call("/v1/chat/completions", chat, brand.HeaderTags, "team:search, has space,ok=1, <b>"); status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	recs := w.Records()
	if tags := recs[len(recs)-1].Meta.(*resolver.Meta).Tags; len(tags) != 2 || tags[0] != "team:search" || tags[1] != "ok=1" {
		t.Fatalf("tags %v", tags)
	}
}

// An agent's tool loop keeps its turn's tier whichever replica serves each
// call: the turn's placement goes through Valkey.
func TestTurnsAcrossReplicas(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	w.Grant(catalog.Routers)
	w.Serves("main", "gpt-5-nano", "gpt-5-mini")
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-opus-5-5"})
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Active,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-nano"}, {Name: "smart", Model: "claude-opus-5-5"}},
			Rules: []tiers.Rule{{When: "request.messages == 1", Tier: "fast"}}}}); err != nil {
		t.Fatal(err)
	}
	other := w.Replica()
	start := `{"model":"auto","messages":[{"role":"user","content":"list the files"}]}`
	w.Eventually(func() bool {
		_, h, _ := w.Call("/v1/chat/completions", start)
		return h.Get(brand.HeaderRouter) == "auto"
	})
	if _, h, _ := w.Call("/v1/chat/completions", start); h.Get(brand.HeaderTier) != "fast" {
		t.Fatalf("the turn's start: %s", h.Get(brand.HeaderTier))
	}
	// The tool loop: three calls the rule no longer holds for, on the
	// other replica, then on this one.
	loop := `{"model":"auto","messages":[{"role":"user","content":"list the files"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"a.go b.go"}]}`
	// The placement reaches Valkey in the store's next batch (every 10
	// ms). A call to the other replica before it would place the turn
	// itself, and the first placement wins: an agent's next call comes
	// after its tool ran, never this soon.
	time.Sleep(100 * time.Millisecond)
	w.Eventually(func() bool {
		_, h, _ := w.CallOn(other, "/v1/chat/completions", loop)
		return h.Get(brand.HeaderTier) == "fast"
	})
	for _, srv := range []*httptest.Server{other, w.Gateway, other} {
		if status, h, out := w.CallOn(srv, "/v1/chat/completions", loop); status != 200 || h.Get(brand.HeaderTier) != "fast" ||
			h.Get(brand.HeaderTierSource) != "rule" {
			t.Fatalf("%d %s: tier %s (%s)", status, out, h.Get(brand.HeaderTier), h.Get(brand.HeaderTierSource))
		}
	}
	// The next human message starts a turn of its own.
	next := `{"model":"auto","messages":[{"role":"user","content":"list the files"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"a.go b.go"},{"role":"assistant","content":"a.go and b.go"},
		{"role":"user","content":"is b.go safe to delete?"}]}`
	if _, h, _ := w.CallOn(other, "/v1/chat/completions", next); h.Get(brand.HeaderTier) != "smart" {
		t.Fatalf("the next turn: %s", h.Get(brand.HeaderTier))
	}
}

// A plain route's turn that fell back keeps the fallback for the rest of
// the turn (an agent's tool loop), where its prompt is cached, though the
// route's first model answers again; the next human message starts from
// the first model.
func TestTurnsKeepTheirFallback(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	w.Provider("local", providers.FormatOpenAI, []string{"llama-local"})
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default",
		Fallbacks: []string{"local/llama-local"}}); err != nil {
		t.Fatal(err)
	}
	start := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"list the files"}]}`
	w.Eventually(func() bool { // the route reached the gateway
		w.Fake.Fail(503)
		_, h, _ := w.Call("/v1/chat/completions", start)
		return h.Get(brand.HeaderProvider) == "local"
	})
	time.Sleep(100 * time.Millisecond) // the call's turn is recorded off its path
	loop := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"list the files"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"a.go b.go"}]}`
	if _, h, _ := w.Call("/v1/chat/completions", loop); h.Get(brand.HeaderProvider) != "local" {
		t.Fatalf("the tool loop went to %q, not the turn's fallback", h.Get(brand.HeaderProvider))
	}
	next := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"list the files"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"a.go b.go"},{"role":"assistant","content":"a.go and b.go"},
		{"role":"user","content":"is b.go safe to delete?"}]}`
	if _, h, _ := w.Call("/v1/chat/completions", next); h.Get(brand.HeaderProvider) != "main" {
		t.Fatalf("the next turn went to %q, not the route's first model", h.Get(brand.HeaderProvider))
	}
}

// A model's learned profile decides how its calls are cached: a custom
// endpoint's model that caches what a request marks gets breakpoints.
func TestCallsFollowTheirModelsProfile(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	ctx := context.Background()
	w.Grant(catalog.PromptCaching)
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.App.CacheProfiles.Save(ctx, &cacheprofile.Doc{Models: map[string]cacheprofile.Profile{
		cacheprofile.Key("custom", "llama-cached"): {Use: promptcache.Markers},
	}}); err != nil {
		t.Fatal(err)
	}
	system, _ := json.Marshal(strings.Repeat("Answer as the house style says. ", 200))
	body := func(model string) string {
		return `{"model":"` + model + `","messages":[{"role":"system","content":` + string(system) + `},{"role":"user","content":"hi"}]}`
	}
	w.Eventually(func() bool { // the caching route reached the gateway
		_, _, _ = w.Call("/v1/chat/completions", body("llama-cached"))
		recs := w.Records()
		return len(recs) > 0 && recs[len(recs)-1].CacheStrategy == promptcache.Markers
	})
	reqs := w.Fake.Requests()
	if sent := string(reqs[len(reqs)-1].Body); !strings.Contains(sent, `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("a model whose profile marks: %s", sent)
	}
	_, _, _ = w.Call("/v1/chat/completions", body("llama-plain"))
	reqs = w.Fake.Requests()
	if sent := string(reqs[len(reqs)-1].Body); strings.Contains(sent, "cache_control") {
		t.Fatalf("a model without a profile was marked: %s", sent)
	}
}

// A provider that refuses breakpoints answers the call sent again without
// them, and its model's next calls go unmarked.
func TestRefusedMarkersStayOff(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{Upstream: fakeupstream.Options{RefuseMarkers: true}})
	ctx := context.Background()
	w.Grant(catalog.PromptCaching)
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.App.CacheProfiles.Save(ctx, &cacheprofile.Doc{Models: map[string]cacheprofile.Profile{
		cacheprofile.Key("custom", "llama-plain"): {Use: promptcache.Markers},
	}}); err != nil {
		t.Fatal(err)
	}
	system, _ := json.Marshal(strings.Repeat("Answer as the house style says. ", 200))
	body := `{"model":"llama-plain","messages":[{"role":"system","content":` + string(system) + `},{"role":"user","content":"hi"}]}`
	w.Eventually(func() bool { // the caching route reached the gateway
		status, _, _ := w.Call("/v1/chat/completions", body)
		recs := w.Records()
		return status == 200 && len(recs) > 0 && recs[len(recs)-1].Healed == gateway.HealUnmarked
	})
	before := len(w.Fake.Requests())
	if status, _, out := w.Call("/v1/chat/completions", body); status != 200 {
		t.Fatalf("%d %s", status, out)
	}
	reqs := w.Fake.Requests()
	recs := w.Records()
	if len(reqs) != before+1 || strings.Contains(string(reqs[len(reqs)-1].Body), "cache_control") ||
		recs[len(recs)-1].CacheStrategy != promptcache.Auto || recs[len(recs)-1].Healed != "" {
		t.Fatalf("the next call: %d sent, record %+v", len(reqs)-before, recs[len(recs)-1])
	}
}

// A project's own endpoint is planned by its price list alone: what every
// project's calls taught the profiles doesn't reach it, nor its calls the
// profiles.
func TestOwnEndpointsAreNotPooled(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{OwnEndpoints: true})
	ctx := context.Background()
	w.Grant(catalog.PromptCaching)
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.App.CacheProfiles.Save(ctx, &cacheprofile.Doc{Models: map[string]cacheprofile.Profile{
		cacheprofile.Key("custom", "llama-cached"): {Use: promptcache.Markers},
	}}); err != nil {
		t.Fatal(err)
	}
	system, _ := json.Marshal(strings.Repeat("Answer as the house style says. ", 200))
	body := `{"model":"llama-cached","messages":[{"role":"system","content":` + string(system) + `},{"role":"user","content":"hi"}]}`
	w.Eventually(func() bool { // the caching route reached the gateway
		_, _, _ = w.Call("/v1/chat/completions", body)
		recs := w.Records()
		return len(recs) > 0 && recs[len(recs)-1].CacheStrategy != ""
	})
	reqs, recs := w.Fake.Requests(), w.Records()
	if rec := recs[len(recs)-1]; strings.Contains(string(reqs[len(reqs)-1].Body), "cache_control") ||
		rec.CacheStrategy != promptcache.Auto || rec.Pooled {
		t.Fatalf("an endpoint of the project's own: %+v", rec)
	}
}
