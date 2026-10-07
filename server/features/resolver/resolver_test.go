package resolver_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel/pkg/apikeys"

	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/budgets"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/resolver"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/routes"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/rules"
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
