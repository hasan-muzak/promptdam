package resolver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/apikeys"
	"github.com/hasan-muzak/keel/pkg/config"
	"github.com/hasan-muzak/keel/pkg/counter"
	"github.com/hasan-muzak/keel/pkg/entx"
	"github.com/hasan-muzak/keel/pkg/identity"
	"github.com/hasan-muzak/keel/pkg/jobs"
	"github.com/hasan-muzak/keel/pkg/keel"
	"github.com/hasan-muzak/keel/pkg/safehttp"
	"github.com/hasan-muzak/keel/pkg/testkit"

	"github.com/hasan-muzak/keel/products/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
	"github.com/hasan-muzak/keel/products/promptdam/server/core"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/budgets"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/gateway"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/resolver"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/routes"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/snapshot"
	"github.com/hasan-muzak/keel/products/promptdam/server/migrations"
)

type world struct {
	t                 *testing.T
	ctx               context.Context
	c                 *keel.Core
	app               *core.App
	org, project, key uuid.UUID
	raw               string
	fake              *fakeupstream.Server
	upstream, gw      *httptest.Server

	mu      sync.Mutex
	records []gateway.Record
}

func setup(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	d := testkit.Postgres(t, migrations.Postgres())
	if err := jobs.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	cfg := keel.Config{Base: config.Base{Env: config.Development, APIKeyPepper: strings.Repeat("p", 32)}, MailURL: "smtp://localhost:1025"}
	c, err := keel.New(ctx, cfg, core.Product, d, testkit.Valkey(t))
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, ctx: ctx, c: c, app: core.NewApp(c)}
	u, err := c.Tenancy.SignIn(ctx, identity.Claims{UID: "fb-acme", Email: "acme@example.com", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	org, err := c.Tenancy.CreateOrg(ctx, u.ID, "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	prj, err := c.Tenancy.CreateProject(ctx, org.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	w.org, w.project = org.ID, prj.ID
	rec, raw, err := c.APIKeys.Issue(ctx, apikeys.IssueInput{Org: org.ID, Project: &prj.ID, Name: "app", Kind: apikeys.Secret,
		Scopes: []string{core.ScopeGateway}})
	if err != nil {
		t.Fatal(err)
	}
	w.key, w.raw = rec.ID, raw

	w.fake = fakeupstream.New(fakeupstream.Options{})
	w.upstream = httptest.NewServer(w.fake)
	t.Cleanup(w.upstream.Close)
	w.provider("main", "openai", nil)

	snaps := snapshot.NewStore(c.KV, w.app.Sources())
	rates := counter.New(c.KV, "rpm", counter.Options{})
	for _, l := range []interface {
		Start(context.Context) error
		Stop(context.Context) error
	}{snaps, rates, w.app.SpendCounters} {
		if err := l.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Stop(context.Background()) })
	}
	res := resolver.New(resolver.Config{
		Keys: c.APIKeys, Snapshots: snaps, Quota: c.Metering, Spend: w.app.Spend, Rates: rates, Prices: w.app.Prices,
		Scope: core.ScopeGateway,
	})
	w.gw = httptest.NewServer(gateway.New(gateway.Config{
		Resolver: res, Transport: safehttp.NewTransport(safehttp.TransportOptions{AllowLoopback: true}),
		Done: func(r *gateway.Record) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.records = append(w.records, *r)
		},
	}))
	t.Cleanup(w.gw.Close)
	return w
}

func (w *world) scoped() context.Context { return entx.WithOrg(w.ctx, w.org) }

// provider adds a custom OpenAI-format provider at the fake upstream.
func (w *world) provider(name, format string, models []string) {
	w.t.Helper()
	key := "sk-" + name + "-0000000000"
	_, err := w.app.Providers.Create(w.scoped(), w.org, w.project, providers.Input{
		Name: name, Kind: providers.Custom, Format: format, BaseURL: w.upstream.URL + "/v1", APIKey: &key, Models: models,
		Enabled: true,
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) call(path, body string, header ...string) (int, http.Header, string) {
	w.t.Helper()
	req, _ := http.NewRequestWithContext(w.ctx, http.MethodPost, w.gw.URL+path, strings.NewReader(body))
	if path == "/v1/models" {
		req, _ = http.NewRequestWithContext(w.ctx, http.MethodGet, w.gw.URL+path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+w.raw)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return res.StatusCode, res.Header, string(out)
}

// eventually retries a check while a change reaches the gateway.
func (w *world) eventually(check func() bool) {
	w.t.Helper()
	for range 200 {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.t.Fatal("the change never reached the gateway")
}

const chat = `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`

func TestCallsAreAuthenticatedAndRouted(t *testing.T) {
	w := setup(t)
	status, h, body := w.call("/v1/chat/completions", chat)
	if status != 200 || h.Get(brand.HeaderProvider) != "main" || !strings.Contains(body, "tok0") {
		t.Fatalf("%d %v %s", status, h, body)
	}
	// The provider got the provider's key, never the client's.
	if got := w.fake.Requests()[0].Header.Get("Authorization"); got != "Bearer sk-main-0000000000" {
		t.Fatalf("the provider got %q", got)
	}
	w.mu.Lock()
	m, ok := w.records[0].Meta.(*resolver.Meta)
	w.mu.Unlock()
	if !ok || m.Project != w.project || m.Key != w.key || m.Org != w.org {
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
		if status, h, body := w.call("/v1/chat/completions", chat, tt.header...); status != tt.status ||
			h.Get(brand.HeaderErrorCode) != tt.code {
			t.Errorf("%v: %d %s", tt.header, status, body)
		}
	}
	// Anthropic's SDKs send the key in x-api-key; this project has no
	// Anthropic-format provider.
	status, h, _ = w.call("/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":5,"messages":[]}`,
		"Authorization", "", "X-Api-Key", w.raw)
	if status != 404 || h.Get(brand.HeaderErrorCode) != "no_provider" {
		t.Fatalf("%d %s", status, h.Get(brand.HeaderErrorCode))
	}

	// A key without the gateway scope can't call.
	_, other, err := w.c.APIKeys.Issue(w.ctx, apikeys.IssueInput{Org: w.org, Project: &w.project, Name: "x", Kind: apikeys.Secret})
	if err != nil {
		t.Fatal(err)
	}
	if status, h, _ := w.call("/v1/chat/completions", chat, "Authorization", "Bearer "+other); status != 403 ||
		h.Get(brand.HeaderErrorCode) != "key_not_for_gateway" {
		t.Fatalf("%d %s", status, h.Get(brand.HeaderErrorCode))
	}
}

func TestKeySettingsApplyWithinMoments(t *testing.T) {
	w := setup(t)
	if _, err := w.app.Keys.Put(w.scoped(), w.org, w.project, w.key, keysettings.Settings{
		AllowedModels: []string{"gpt-5-nano"}, RPM: 3, Developer: "ana",
	}); err != nil {
		t.Fatal(err)
	}
	w.eventually(func() bool {
		status, h, _ := w.call("/v1/chat/completions", chat)
		return status == 403 && h.Get(brand.HeaderErrorCode) == "model_not_allowed"
	})
	nano := strings.Replace(chat, "gpt-5-mini", "gpt-5-nano", 1)
	codes := map[int]int{}
	for range 4 {
		status, _, _ := w.call("/v1/chat/completions", nano)
		codes[status]++
	}
	if codes[200] != 3 || codes[429] != 1 {
		t.Fatalf("with 3 requests a minute: %v", codes)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if m := w.records[len(w.records)-2].Meta.(*resolver.Meta); m.Developer != "ana" {
		t.Fatalf("meta %+v", m)
	}
}

func TestBlockingBudget(t *testing.T) {
	w := setup(t)
	if _, err := w.app.Budgets.Create(w.scoped(), w.org, w.project, budgets.Input{
		Name: "cap", Scope: budgets.ScopeKey, Target: w.key.String(), Window: "day", LimitMicros: 1000, Action: "block",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := w.call("/v1/chat/completions", chat); status != 200 {
		t.Fatalf("before spending: %d", status)
	}
	w.app.Spend.Add(budgets.Who{Project: w.project, Key: w.key}, 1000)
	w.eventually(func() bool {
		status, h, _ := w.call("/v1/chat/completions", chat)
		return status == 429 && h.Get(brand.HeaderErrorCode) == "budget_exceeded" && h.Get("Retry-After") != ""
	})
}

func TestRoutesFallBack(t *testing.T) {
	w := setup(t)
	w.provider("backup", "openai", []string{"gpt-5-nano"})
	if _, err := w.app.Routes.Create(w.scoped(), w.org, w.project, routes.Input{
		Name: routes.Default, Fallbacks: []string{"backup/gpt-5-nano"}, Retries: 0,
	}); err != nil {
		t.Fatal(err)
	}
	w.eventually(func() bool {
		w.fake.Fail(http.StatusServiceUnavailable)
		status, h, _ := w.call("/v1/chat/completions", chat)
		return status == 200 && h.Get(brand.HeaderProvider) == "backup" && h.Get(brand.HeaderServedModel) == "gpt-5-nano"
	})
}

func TestModels(t *testing.T) {
	w := setup(t)
	w.provider("claude", "anthropic", []string{"claude-haiku-4-5", "claude-sonnet-*"})
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	w.eventually(func() bool {
		status, _, body := w.call("/v1/models", "", "Authorization", "", "X-Api-Key", w.raw, "Anthropic-Version", "2023-06-01")
		return status == 200 && json.Unmarshal([]byte(body), &list) == nil && len(list.Data) == 1 &&
			list.Data[0].ID == "claude-haiku-4-5"
	})
	status, _, body := w.call("/v1/models", "")
	if status != 200 || !strings.Contains(body, `"object":"list"`) {
		t.Fatalf("%d %s", status, body)
	}
}
