package routers_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	muzak "muzak.dev/framework"
	"muzak.dev/framework/testclient"

	"github.com/hasan-muzak/keel-core/pkg/apikeys"
	"github.com/hasan-muzak/keel-core/pkg/config"
	"github.com/hasan-muzak/keel-core/pkg/ent/orgflag"
	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/keel"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/core"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/handlers"
	"github.com/hasan-muzak/promptdam/server/migrations"
	"github.com/hasan-muzak/promptdam/server/routers"
)

type env struct {
	c      *keel.Core
	app    *core.App
	api    *muzak.App
	client *testclient.Client
	issuer *testkit.FirebaseIssuer
}

func setup(t *testing.T) *env { t.Helper(); return setupWith(t, false) }

// setupWith starts the API, with a test ClickHouse when events is set.
func setupWith(t *testing.T, events bool) *env {
	t.Helper()
	cfg := keel.Config{Base: config.Base{Env: config.Development, APIKeyPepper: strings.Repeat("p", 32)}, MailURL: "smtp://localhost:1025"}
	d := testkit.Postgres(t, migrations.Postgres())
	if err := jobs.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	c, err := keel.New(context.Background(), cfg, core.Product, d, testkit.Valkey(t))
	if err != nil {
		t.Fatal(err)
	}
	if events {
		c.Events = testkit.ClickHouse(t, migrations.ClickHouse())
	}
	iss := testkit.NewFirebaseIssuer(t, "demo-keel")
	c.Verifier = iss.Verifier()
	a := c.API("test")
	app := core.NewApp(c)
	routers.Mount(a, app)
	return &env{c: c, app: app, api: a, client: testclient.New(t, a), issuer: iss}
}

// plan gives the organization exactly these plan features, as a plan
// change would (none: back to Free).
func (e *env) plan(t *testing.T, org string, features ...string) {
	t.Helper()
	ctx := entx.SkipTenant(context.Background())
	id := mustID(t, ids.Org, org)
	e.c.DB.Ent.OrgFlag.Delete().Where(orgflag.OrgID(id)).ExecX(ctx)
	for _, f := range features {
		e.c.DB.Ent.OrgFlag.Create().SetOrgID(id).SetKey("feature." + f).SetValue(json.RawMessage(`true`)).SaveX(ctx)
	}
	e.c.Tenancy.ForgetOrg(context.Background(), id)
	e.c.Billing.BustEntitlements(context.Background(), id)
}

func (e *env) as(uid string) testclient.RequestOption {
	return testclient.Header("Authorization", "Bearer "+e.issuer.Token(uid, testkit.TokenOptions{Email: uid + "@example.com", Name: uid}))
}

// project creates an organization and a project for uid, and returns the
// project's path.
func (e *env) project(uid string) (org platform.OrgOut, prj platform.ProjectOut, path string) {
	who := e.as(uid)
	org = testclient.Decoded[platform.OrgOut](e.client.Post("/v1/orgs", who, testclient.JSON(map[string]string{"name": uid + " Inc"})))
	prj = testclient.Decoded[platform.ProjectOut](e.client.Post("/v1/orgs/"+org.ID+"/projects", who,
		testclient.JSON(map[string]string{"name": "Web"})))
	return org, prj, "/v1/orgs/" + org.ID + "/projects/" + prj.ID
}

func TestProviders(t *testing.T) {
	e := setup(t)
	alice := e.as("alice")
	_, _, base := e.project("alice")
	path := base + "/providers"
	changed := 0
	e.app.OnChanged(func(context.Context, uuid.UUID) { changed++ })

	kinds := testclient.Decoded[handlers.ProviderKindsOut](e.client.Get("/v1/provider-kinds", alice).AssertStatus(http.StatusOK))
	if len(kinds.Data) < 10 {
		t.Fatalf("kinds = %+v", kinds)
	}

	created := e.client.Post(path, alice, testclient.JSON(map[string]any{
		"name": "openai", "kind": "openai", "api_key": "sk-test-secret-abcd",
	})).AssertStatus(http.StatusCreated)
	p := testclient.Decoded[handlers.ProviderOut](created)
	if p.APIFormat != "openai" || p.BaseURL != "https://api.openai.com/v1" || !p.KeySet || p.KeyHint != "…abcd" ||
		!p.Enabled || changed != 1 {
		t.Fatalf("created %+v (changed %d)", p, changed)
	}
	listed := e.client.Get(path, alice).AssertStatus(http.StatusOK)
	for _, body := range []string{created.String(), listed.String()} {
		if strings.Contains(body, "sk-test-secret") {
			t.Fatalf("a response holds the key: %s", body)
		}
	}
	// The key is stored sealed.
	var stored string
	if err := e.c.DB.SQL.QueryRow(`SELECT secrets FROM providers`).Scan(&stored); err != nil || strings.Contains(stored, "sk-test") {
		t.Fatalf("stored %q, %v", stored, err)
	}

	for _, tt := range []struct {
		body   map[string]any
		status int
		code   string
	}{
		{map[string]any{"name": "openai", "kind": "openai", "api_key": "sk-x"}, http.StatusConflict, handlers.CodeProviderNameTaken},
		{map[string]any{"name": "o2", "kind": "openai"}, http.StatusUnprocessableEntity, handlers.CodeAPIKeyRequired},
		{map[string]any{"name": "Bad Name", "kind": "openai", "api_key": "sk-x"}, http.StatusUnprocessableEntity, handlers.CodeInvalidProvider},
		{map[string]any{"name": "x", "kind": "nope", "api_key": "sk-x"}, http.StatusUnprocessableEntity, handlers.CodeInvalidProvider},
		{map[string]any{"name": "x", "kind": "custom", "api_format": "openai"}, http.StatusUnprocessableEntity, handlers.CodeInvalidProvider},
		// Internal addresses are refused, cloud metadata included.
		{map[string]any{"name": "x", "kind": "custom", "api_format": "openai", "base_url": "https://169.254.169.254/v1"},
			http.StatusUnprocessableEntity, handlers.CodeInvalidProvider},
		{map[string]any{"name": "x", "kind": "custom", "api_format": "openai", "base_url": "https://10.0.0.8/v1"},
			http.StatusUnprocessableEntity, handlers.CodeInvalidProvider},
		{map[string]any{"name": "x", "kind": "custom", "api_format": "openai", "base_url": "https://u:p@example.com/v1"},
			http.StatusUnprocessableEntity, handlers.CodeInvalidProvider},
	} {
		res := e.client.Post(path, alice, testclient.JSON(tt.body))
		if res.Status != tt.status || res.Error().Error.Code != tt.code {
			t.Errorf("%v: %d %s", tt.body, res.Status, res.String())
		}
	}

	// A stored key stays with its endpoint: moving it needs the key again.
	moved := e.client.Put(path+"/"+p.ID, alice, testclient.JSON(map[string]any{
		"name": "openai", "kind": "openai", "base_url": "https://eu.api.openai.com/v1",
	}))
	if moved.Status != http.StatusUnprocessableEntity || moved.Error().Error.Code != handlers.CodeAPIKeyRequired {
		t.Fatalf("moved without the key: %d %s", moved.Status, moved.String())
	}
	kept := testclient.Decoded[handlers.ProviderOut](e.client.Put(path+"/"+p.ID, alice, testclient.JSON(map[string]any{
		"name": "openai", "kind": "openai", "priority": 5, "models": []string{"gpt-5-mini", " gpt-5-mini "},
	})).AssertStatus(http.StatusOK))
	if !kept.KeySet || kept.KeyHint != "…abcd" || kept.Priority != 5 || len(kept.Models) != 1 {
		t.Fatalf("kept %+v", kept)
	}

	// The check calls the provider's model list with the key.
	var gotAuth string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" || gotAuth != "Bearer sk-local-key-0001" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer fake.Close()
	local := testclient.Decoded[handlers.ProviderOut](e.client.Post(path, alice, testclient.JSON(map[string]any{
		"name": "local", "kind": "custom", "api_format": "openai", "base_url": fake.URL + "/v1/", "api_key": "sk-local-key-0001",
	})).AssertStatus(http.StatusCreated))
	checked := testclient.Decoded[handlers.ProviderOut](e.client.Post(path+"/"+local.ID+"/check", alice).AssertStatus(http.StatusOK))
	if checked.CheckedAt == nil || checked.CheckError != "" || gotAuth != "Bearer sk-local-key-0001" {
		t.Fatalf("checked %+v (auth %q)", checked, gotAuth)
	}
	refused := testclient.Decoded[handlers.ProviderOut](e.client.Put(path+"/"+local.ID, alice, testclient.JSON(map[string]any{
		"name": "local", "kind": "custom", "api_format": "openai", "base_url": fake.URL + "/v1", "api_key": "sk-wrong-key-0002",
	})).AssertStatus(http.StatusOK))
	if refused.CheckedAt != nil {
		t.Fatal("a new key keeps the old key's check")
	}
	refused = testclient.Decoded[handlers.ProviderOut](e.client.Post(path+"/"+local.ID+"/check", alice).AssertStatus(http.StatusOK))
	if refused.CheckError != "the provider refused the key" {
		t.Fatalf("check error %q", refused.CheckError)
	}

	e.client.Delete(path+"/"+local.ID, alice).AssertStatus(http.StatusNoContent)
	e.client.Get(path+"/"+local.ID, alice).AssertStatus(http.StatusNotFound)
	e.client.Get(path+"/prv_nope", alice).AssertStatus(http.StatusNotFound)
	e.client.Get(path, e.as("mallory")).AssertStatus(http.StatusNotFound)
}

func TestKeySettings(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	alice := e.as("alice")
	org, prj, base := e.project("alice")
	key := testclient.Decoded[platform.CreatedKeyOut](e.client.Post(base+"/keys", alice,
		testclient.JSON(map[string]any{"name": "app", "kind": "secret", "scopes": []string{core.ScopeGateway}})).
		AssertStatus(http.StatusCreated))
	if !strings.HasPrefix(key.Key, "pd_sk_") {
		t.Fatalf("key %q", key.Key)
	}
	path := base + "/keys/" + key.ID + "/gateway"

	got := testclient.Decoded[handlers.KeySettings](e.client.Get(path, alice).AssertStatus(http.StatusOK))
	if len(got.AllowedModels) != 0 || got.RPM != 0 || got.AllowedModels == nil {
		t.Fatalf("defaults %+v", got)
	}
	settings := map[string]any{
		"allowed_models": []string{"gpt-5-mini", "claude-*", "gpt-5-mini"}, "tags": []string{"team:search"},
		"developer": "ana@acme.test", "rpm": 600, "route": "default",
	}
	// Naming a key's developer comes with per-developer keys (Team).
	if res := e.client.Put(path, alice, testclient.JSON(settings)); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Free names a developer: %d %s", res.Status, res.String())
	}
	e.plan(t, org.ID, catalog.DeveloperKeys)
	set := testclient.Decoded[handlers.KeySettings](e.client.Put(path, alice, testclient.JSON(settings)).AssertStatus(http.StatusOK))
	if len(set.AllowedModels) != 2 || set.Developer != "ana@acme.test" || set.RPM != 600 {
		t.Fatalf("set %+v", set)
	}
	if again := testclient.Decoded[handlers.KeySettings](e.client.Get(path, alice)); again.RPM != 600 || len(again.Tags) != 1 {
		t.Fatalf("not stored: %+v", again)
	}
	// After a downgrade the developer stays, and the rest can change.
	e.plan(t, org.ID)
	settings["rpm"] = 300
	e.client.Put(path, alice, testclient.JSON(settings)).AssertStatus(http.StatusOK)
	settings["developer"] = "bo@acme.test"
	if res := e.client.Put(path, alice, testclient.JSON(settings)); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Free names another developer: %d %s", res.Status, res.String())
	}
	settings["developer"], settings["rpm"] = "ana@acme.test", 600
	e.client.Put(path, alice, testclient.JSON(settings)).AssertStatus(http.StatusOK)
	for _, body := range []map[string]any{
		{"allowed_models": []string{"gpt*-x"}}, {"tags": []string{"has space"}}, {"route": "Bad Route"},
	} {
		if res := e.client.Put(path, alice, testclient.JSON(body)); res.Status != http.StatusUnprocessableEntity ||
			res.Error().Error.Code != handlers.CodeInvalidKeySettings {
			t.Errorf("%v: %d %s", body, res.Status, res.String())
		}
	}

	// A rotated key's replacement keeps the settings.
	rotated := testclient.Decoded[platform.CreatedKeyOut](e.client.Post(base+"/keys/"+key.ID+"/rotate", alice).
		AssertStatus(http.StatusOK))
	kept := testclient.Decoded[handlers.KeySettings](e.client.Get(base+"/keys/"+rotated.ID+"/gateway", alice).
		AssertStatus(http.StatusOK))
	if kept.RPM != 600 || kept.Developer != "ana@acme.test" {
		t.Fatalf("the replacement lost the settings: %+v", kept)
	}

	// A key that can't call the gateway has no gateway settings.
	orgID, prjID := mustID(t, ids.Org, org.ID), mustID(t, ids.Project, prj.ID)
	other, _, err := e.c.APIKeys.Issue(ctx, apikeys.IssueInput{Org: orgID, Project: &prjID, Name: "x", Kind: apikeys.Secret})
	if err != nil {
		t.Fatal(err)
	}
	res := e.client.Put(base+"/keys/"+ids.Encode(ids.APIKey, other.ID)+"/gateway", alice, testclient.JSON(map[string]any{}))
	if res.Status != http.StatusUnprocessableEntity || res.Error().Error.Code != handlers.CodeKeyNotForGateway {
		t.Fatalf("a key without the scope: %d %s", res.Status, res.String())
	}
	e.client.Get(base+"/keys/key_nope/gateway", alice).AssertStatus(http.StatusNotFound)
}

func mustID(t *testing.T, prefix, s string) uuid.UUID {
	t.Helper()
	id, err := ids.Decode(prefix, s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRoutes(t *testing.T) {
	e := setup(t)
	alice := e.as("alice")
	org, _, base := e.project("alice")
	path := base + "/routes"
	r := testclient.Decoded[handlers.RouteOut](e.client.Post(path, alice, testclient.JSON(map[string]any{
		"name": "default", "fallbacks": []string{"gpt-5-mini", "openrouter/anthropic/claude-haiku-4-5"},
	})).AssertStatus(http.StatusCreated))
	if r.Retries != 1 || len(r.Fallbacks) != 2 {
		t.Fatalf("created %+v", r)
	}
	for _, body := range []map[string]any{
		{"name": "default", "fallbacks": []string{}},
		{"name": "x", "fallbacks": []string{"/gpt"}},
		{"name": "x", "retries": 9},
		{"name": "Bad", "fallbacks": []string{}},
	} {
		if res := e.client.Post(path, alice, testclient.JSON(body)); res.Status != http.StatusConflict &&
			res.Status != http.StatusUnprocessableEntity {
			t.Errorf("%v: %d %s", body, res.Status, res.String())
		}
	}
	zero := 0
	up := testclient.Decoded[handlers.RouteOut](e.client.Put(path+"/"+r.ID, alice, testclient.JSON(map[string]any{
		"name": "default", "fallbacks": []string{"gpt-5-nano"}, "retries": zero,
	})).AssertStatus(http.StatusOK))
	if up.Retries != 0 || up.Fallbacks[0] != "gpt-5-nano" {
		t.Fatalf("updated %+v", up)
	}
	// Prompt caching comes with Pro; a route keeps it after a downgrade.
	cached := map[string]any{"name": "default", "fallbacks": []string{"gpt-5-nano"}, "prompt_cache": true}
	if res := e.client.Put(path+"/"+r.ID, alice, testclient.JSON(cached)); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Free caches prompts: %d %s", res.Status, res.String())
	}
	e.plan(t, org.ID, catalog.PromptCaching)
	e.client.Put(path+"/"+r.ID, alice, testclient.JSON(cached)).AssertStatus(http.StatusOK)
	e.plan(t, org.ID)
	cached["retries"] = 2
	if kept := testclient.Decoded[handlers.RouteOut](e.client.Put(path+"/"+r.ID, alice, testclient.JSON(cached)).
		AssertStatus(http.StatusOK)); !kept.PromptCache {
		t.Fatalf("kept %+v", kept)
	}
	if res := e.client.Post(path, alice, testclient.JSON(map[string]any{"name": "batch", "fallbacks": []string{},
		"prompt_cache": true})); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Free creates a caching route: %d %s", res.Status, res.String())
	}
	e.client.Delete(path+"/"+r.ID, alice).AssertStatus(http.StatusNoContent)
	e.client.Get(path+"/"+r.ID, alice).AssertStatus(http.StatusNotFound)
}

func TestRules(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	alice := e.as("alice")
	org, _, base := e.project("alice")
	orgID := mustID(t, ids.Org, org.ID)
	path := base + "/rules"
	short := map[string]any{"name": "short", "when": "request.input_tokens < 1500", "to_model": "gpt-5-nano"}

	// Rules come with Pro; Free is refused.
	if res := e.client.Post(path, alice, testclient.JSON(short)); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Free: %d %s", res.Status, res.String())
	}
	e.c.DB.Ent.OrgFlag.Create().SetOrgID(orgID).SetKey("feature." + catalog.RoutingRules).SetValue(json.RawMessage(`true`)).
		SaveX(entx.SkipTenant(ctx))
	e.c.Tenancy.ForgetOrg(ctx, orgID)
	e.c.Billing.BustEntitlements(ctx, orgID)

	// A condition that doesn't compile says where.
	res := e.client.Post(path, alice, testclient.JSON(map[string]any{
		"name": "bad", "when": "request.input_tokens <\n  request.nope", "to_model": "gpt-5-nano",
	}))
	if er := res.Error().Error; res.Status != http.StatusUnprocessableEntity || er.Code != handlers.CodeInvalidRule ||
		len(er.Details) == 0 || er.Details[0].Field != "when" || !strings.HasPrefix(er.Details[0].Issue, "line 2,") {
		t.Fatalf("%d %s", res.Status, res.String())
	}

	rule := testclient.Decoded[handlers.RuleOut](e.client.Post(path, alice, testclient.JSON(short)).
		AssertStatus(http.StatusCreated))
	if rule.Status != "draft" || rule.ActivatedAt != nil || !strings.HasPrefix(rule.ID, handlers.RulePrefix+"_") {
		t.Fatalf("created %+v", rule)
	}
	if res := e.client.Post(path, alice, testclient.JSON(short)); res.Status != http.StatusConflict ||
		res.Error().Error.Code != handlers.CodeRuleNameTaken {
		t.Fatalf("same name: %d %s", res.Status, res.String())
	}

	// Dry-runs, of a rule not saved and of a saved one.
	dry := testclient.Decoded[handlers.DryRunOut](e.client.Post(path+"/dry-run", alice, testclient.JSON(map[string]any{
		"when": "request.has_tools", "to_model": "gpt-5-nano",
	})).AssertStatus(http.StatusOK))
	if dry.Calls != 0 || dry.Note == "" || dry.To.Sub(dry.From) != 7*24*time.Hour {
		t.Fatalf("dry-run %+v", dry)
	}
	if res := e.client.Post(path+"/dry-run", alice, testclient.JSON(map[string]any{
		"when": "request.model", "to_model": "gpt-5-nano",
	})); res.Status != http.StatusUnprocessableEntity || res.Error().Error.Code != handlers.CodeInvalidRule {
		t.Fatalf("dry-run of a string: %d %s", res.Status, res.String())
	}
	if res := e.client.Post(path+"/"+rule.ID+"/dry-run?from=2026-01-01T00:00:00Z&to=2026-03-01T00:00:00Z", alice); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("dry-run of two months: %d %s", res.Status, res.String())
	}
	e.client.Post(path+"/"+rule.ID+"/dry-run", alice).AssertStatus(http.StatusOK)

	// Active, it gets its activation time.
	short["status"], short["priority"] = "active", 10
	got := testclient.Decoded[handlers.RuleOut](e.client.Put(path+"/"+rule.ID, alice, testclient.JSON(short)).
		AssertStatus(http.StatusOK))
	if got.Status != "active" || got.ActivatedAt == nil || got.Priority != 10 {
		t.Fatalf("updated %+v", got)
	}
	list := testclient.Decoded[httpx.List[handlers.RuleOut]](e.client.Get(path, alice).AssertStatus(http.StatusOK))
	if len(list.Data) != 1 || list.Data[0].ID != rule.ID {
		t.Fatalf("list %+v", list)
	}
	e.client.Delete(path+"/"+rule.ID, alice).AssertStatus(http.StatusNoContent)
	e.client.Get(path+"/"+rule.ID, alice).AssertStatus(http.StatusNotFound)
	e.client.Get(path+"/rul_nope", alice).AssertStatus(http.StatusNotFound)
}

func TestRouters(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	alice := e.as("alice")
	org, _, base := e.project("alice")
	orgID := mustID(t, ids.Org, org.ID)
	path := base + "/routers"
	e.client.Post(base+"/providers", alice, testclient.JSON(map[string]any{
		"name": "openai", "kind": "openai", "api_key": "sk-test-secret-abcd",
	})).AssertStatus(http.StatusCreated)
	auto := map[string]any{
		"name": "auto",
		"tiers": []map[string]any{
			{"name": "fast", "model": "gpt-5-nano", "description": "Short questions"},
			{"name": "smart", "model": "gpt-5", "fallbacks": []string{"openai/gpt-5-mini"}},
		},
		"rules": []map[string]any{{"when": "request.messages == 1", "tier": "fast"}},
	}

	// Every plan runs routers in shadow mode, the default.
	rtr := testclient.Decoded[handlers.RouterOut](e.client.Post(path, alice, testclient.JSON(auto)).AssertStatus(http.StatusCreated))
	if rtr.Status != "shadow" || rtr.DefaultTier != "smart" || rtr.EvalBudgetMicros != 100_000_000 || rtr.QualityFloor != 0.95 ||
		rtr.Watching || !rtr.Learned ||
		!strings.HasPrefix(rtr.ID, handlers.RouterPrefix+"_") || len(rtr.Tiers) != 2 || rtr.Tiers[0].Fallbacks == nil {
		t.Fatalf("created %+v", rtr)
	}
	if res := e.client.Post(path, alice, testclient.JSON(auto)); res.Status != http.StatusConflict ||
		res.Error().Error.Code != handlers.CodeRouterNameTaken {
		t.Fatalf("same name: %d %s", res.Status, res.String())
	}
	// Serving traffic needs a plan with model routers.
	auto["status"] = "active"
	if res := e.client.Put(path+"/"+rtr.ID, alice, testclient.JSON(auto)); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Free: %d %s", res.Status, res.String())
	}
	e.c.DB.Ent.OrgFlag.Create().SetOrgID(orgID).SetKey("feature." + catalog.Routers).SetValue(json.RawMessage(`true`)).
		SaveX(entx.SkipTenant(ctx))
	e.c.Billing.BustEntitlements(ctx, orgID)
	// Without evidence that its tiers hold up, only an override activates
	// it.
	if res := e.client.Put(path+"/"+rtr.ID, alice, testclient.JSON(auto)); res.Status != http.StatusConflict ||
		res.Error().Error.Code != handlers.CodeRouterNotReady {
		t.Fatalf("no evidence: %d %s", res.Status, res.String())
	}
	auto["override"] = true
	got := testclient.Decoded[handlers.RouterOut](e.client.Put(path+"/"+rtr.ID, alice, testclient.JSON(auto)).AssertStatus(http.StatusOK))
	if got.Status != "active" || got.ActivatedAt == nil || got.Demoted == nil || got.Watching || got.Learned {
		t.Fatalf("updated %+v", got)
	}
	// After a downgrade it watches; with learned routing, the classifier
	// places its calls.
	e.plan(t, org.ID)
	if w := testclient.Decoded[handlers.RouterOut](e.client.Get(path+"/"+rtr.ID, alice)); !w.Watching || !w.Learned {
		t.Fatalf("downgraded %+v", w)
	}
	e.plan(t, org.ID, catalog.Routers, catalog.RoutingLearned)
	if l := testclient.Decoded[httpx.List[handlers.RouterOut]](e.client.Get(path, alice)); l.Data[0].Watching || !l.Data[0].Learned {
		t.Fatalf("on Team %+v", l.Data[0])
	}
	delete(auto, "override")
	// Active already: edits need no override.
	e.client.Put(path+"/"+rtr.ID, alice, testclient.JSON(auto)).AssertStatus(http.StatusOK)
	if res := e.client.Post(path+"/"+rtr.ID+"/restore", alice, testclient.JSON(map[string]string{"tier": "fast"})); res.Status !=
		http.StatusUnprocessableEntity {
		t.Fatalf("restore a tier not demoted: %d %s", res.Status, res.String())
	}

	for name, tt := range map[string]struct {
		body  map[string]any
		field string
	}{
		"a rule that doesn't compile": {map[string]any{"name": "x", "tiers": []map[string]any{{"name": "a", "model": "m"}},
			"rules": []map[string]any{{"when": "true", "tier": "a"}, {"when": "request.nope", "tier": "a"}}}, "rules[1].when"},
		"a model's name": {map[string]any{"name": "gpt-5", "tiers": []map[string]any{{"name": "a", "model": "m"}}}, ""},
		"no tiers":       {map[string]any{"name": "x", "tiers": []map[string]any{}}, ""},
		"a bad tier":     {map[string]any{"name": "x", "tiers": []map[string]any{{"name": "A B", "model": "m"}}}, ""},
	} {
		res := e.client.Post(path, alice, testclient.JSON(tt.body))
		if res.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, res.Status, res.String())
			continue
		}
		if er := res.Error().Error; tt.field != "" && (len(er.Details) == 0 || er.Details[0].Field != tt.field) {
			t.Errorf("%s: %s", name, res.String())
		}
	}

	// Explain: how the router would place a request, with no provider called.
	ex := testclient.Decoded[handlers.ExplainRouterOut](e.client.Post(path+"/"+rtr.ID+"/explain", alice, testclient.JSON(map[string]any{
		"request": map[string]any{"model": "auto", "messages": []map[string]string{{"role": "user", "content": "hi"}}},
	})).AssertStatus(http.StatusOK))
	if ex.Tier != "fast" || ex.Source != "rule" || ex.Rule == nil || *ex.Rule != 1 || ex.Shadow || len(ex.Tiers) != 2 ||
		ex.Tiers[0].Provider != "openai" || !ex.Tiers[0].Serves || len(ex.Targets) != 3 || ex.Targets[0].Model != "gpt-5-nano" ||
		ex.Targets[2].Model != "gpt-5-mini" {
		t.Fatalf("explain %+v", ex)
	}
	ex = testclient.Decoded[handlers.ExplainRouterOut](e.client.Post(path+"/"+rtr.ID+"/explain", alice, testclient.JSON(map[string]any{
		"api_format": "anthropic.messages", "tier": "smart",
		"request": map[string]any{"model": "auto", "max_tokens": 10, "messages": []map[string]string{{"role": "user", "content": "hi"}}},
	})).AssertStatus(http.StatusOK))
	if ex.Tier != "smart" || ex.Source != "explicit" || !ex.Targets[0].Translated {
		t.Fatalf("explain an Anthropic call %+v", ex)
	}
	if res := e.client.Post(path+"/"+rtr.ID+"/explain", alice, testclient.JSON(map[string]any{
		"tier": "huge", "request": map[string]any{"model": "auto", "messages": []any{}},
	})); res.Status != http.StatusUnprocessableEntity || res.Error().Error.Code != "unknown_tier" {
		t.Fatalf("explain an unknown tier: %d %s", res.Status, res.String())
	}

	// Training can be asked for at once; reports need the request log,
	// which this API runs without.
	tr := testclient.Decoded[handlers.TrainRouterOut](e.client.Post(path+"/"+rtr.ID+"/train", alice).AssertStatus(http.StatusAccepted))
	if !tr.Queued {
		t.Fatalf("train %+v", tr)
	}
	e.client.Get(path+"/"+rtr.ID+"/report", alice).AssertStatus(http.StatusServiceUnavailable)

	// Routes send calls through routers the project has; such a router
	// stays until no route does.
	if res := e.client.Post(base+"/routes", alice, testclient.JSON(map[string]any{"name": "agents", "router": "nope"})); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a route through no router: %d %s", res.Status, res.String())
	}
	rte := testclient.Decoded[handlers.RouteOut](e.client.Post(base+"/routes", alice, testclient.JSON(map[string]any{
		"name": "agents", "router": "auto",
	})).AssertStatus(http.StatusCreated))
	if rte.Router != "auto" {
		t.Fatalf("route %+v", rte)
	}
	if res := e.client.Delete(path+"/"+rtr.ID, alice); res.Status != http.StatusConflict || res.Error().Error.Code != handlers.CodeRouterInUse {
		t.Fatalf("delete in use: %d %s", res.Status, res.String())
	}
	e.client.Delete(base+"/routes/"+rte.ID, alice).AssertStatus(http.StatusNoContent)
	e.client.Delete(path+"/"+rtr.ID, alice).AssertStatus(http.StatusNoContent)
	list := testclient.Decoded[httpx.List[handlers.RouterOut]](e.client.Get(path, alice).AssertStatus(http.StatusOK))
	if len(list.Data) != 0 {
		t.Fatalf("list %+v", list)
	}
}

// A suggested router is built from the project's providers and can be
// created as is.
func TestSuggestRouter(t *testing.T) {
	e := setup(t)
	alice := e.as("alice")
	_, _, base := e.project("alice")
	if res := e.client.Get(base+"/routers/suggest", alice); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("no provider: %d %s", res.Status, res.String())
	}
	for _, p := range []map[string]any{
		{"name": "claude", "kind": "anthropic", "api_key": "sk-ant-test-abcd"},
		{"name": "openai", "kind": "openai", "api_key": "sk-test-secret-abcd", "priority": 1},
	} {
		e.client.Post(base+"/providers", alice, testclient.JSON(p)).AssertStatus(http.StatusCreated)
	}
	s := testclient.Decoded[handlers.SuggestRouterOut](e.client.Get(base+"/routers/suggest", alice).AssertStatus(http.StatusOK))
	r := s.Router
	if r.Name != "auto" || r.Status != "shadow" || len(r.Tiers) != 3 || r.Tiers[0].Model != "claude/claude-haiku-4-5" ||
		r.Tiers[1].Model != "claude/claude-sonnet-5-5" || r.Tiers[2].Model != "claude/claude-opus-5-5" ||
		len(r.Tiers[2].Fallbacks) != 1 || r.Tiers[2].Fallbacks[0] != "openai/gpt-5" || r.DefaultTier != "expert" ||
		r.Judge != "claude/claude-sonnet-5-5" || len(s.Tiers) != 3 || s.Tiers[0].Picked != "family" || len(s.Notes) == 0 {
		t.Fatalf("suggested %+v", s)
	}
	// The model used today becomes the default tier.
	s = testclient.Decoded[handlers.SuggestRouterOut](e.client.Get(base+"/routers/suggest?tiers=2&current_model=gpt-4o", alice).
		AssertStatus(http.StatusOK))
	if s.Router.DefaultTier != "current" || len(s.Router.Tiers) != 3 {
		t.Fatalf("with the current model %+v", s.Router)
	}
	created := testclient.Decoded[handlers.RouterOut](e.client.Post(base+"/routers", alice, testclient.JSON(s.Router)).
		AssertStatus(http.StatusCreated))
	if created.Status != "shadow" || created.DefaultTier != "current" {
		t.Fatalf("created %+v", created)
	}
}

func TestFirewall(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	alice := e.as("alice")
	org, _, base := e.project("alice")
	orgID := mustID(t, ids.Org, org.ID)
	path := base + "/firewall"
	plan := func(features ...string) {
		e.c.DB.Ent.OrgFlag.Delete().Where(orgflag.OrgID(orgID)).ExecX(entx.SkipTenant(ctx))
		for _, f := range features {
			e.c.DB.Ent.OrgFlag.Create().SetOrgID(orgID).SetKey("feature." + f).SetValue(json.RawMessage(`true`)).
				SaveX(entx.SkipTenant(ctx))
		}
		e.c.Tenancy.ForgetOrg(ctx, orgID)
		e.c.Billing.BustEntitlements(ctx, orgID)
	}
	put := func(s map[string]any) *testclient.Response { return e.client.Put(path, alice, testclient.JSON(s)) }

	presets := testclient.Decoded[handlers.FirewallPresetsOut](e.client.Get("/v1/firewall/presets", alice).AssertStatus(http.StatusOK))
	if len(presets.Data) != 2 || presets.Data[0].Class != "secret" || len(presets.Data[1].Detectors) != 7 {
		t.Fatalf("presets %+v", presets)
	}
	for _, p := range presets.Data {
		for _, d := range p.Detectors {
			if d.Description == "" {
				t.Errorf("%s isn't described", d.Name)
			}
		}
	}

	// Until it saves its own, a project blocks secrets and scans answers.
	fw := testclient.Decoded[handlers.FirewallSettings](e.client.Get(path, alice).AssertStatus(http.StatusOK))
	if !fw.Enabled || fw.Secrets != "block" || fw.PII != "off" || !fw.ScanResponses || fw.FailMode != "secrets_closed" ||
		fw.Policies == nil || fw.Custom == nil {
		t.Fatalf("defaults %+v", fw)
	}
	// Free: secrets and alerts on personal data; masking it needs Pro.
	settings := map[string]any{"enabled": true, "secrets": "redact", "pii": "alert", "scan_responses": true}
	put(settings).AssertStatus(http.StatusOK)
	settings["pii"] = "tokenize"
	if res := put(settings); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Free tokenizes: %d %s", res.Status, res.String())
	}
	plan(catalog.PIIRedaction)
	put(settings).AssertStatus(http.StatusOK)
	settings["secrets"] = "tokenize"
	if res := put(settings); res.Status != http.StatusUnprocessableEntity || res.Error().Error.Code != handlers.CodeInvalidFirewall {
		t.Fatalf("tokenized secrets: %d %s", res.Status, res.String())
	}
	settings["secrets"] = "block"

	// Policies come with Team; one that doesn't compile says where.
	policy := map[string]any{"name": "no pii to openai", "when": "classes.pii > 0 &&\n  target.kind == \"openai\"", "action": "block"}
	settings["policies"] = []any{policy}
	if res := put(settings); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Pro sets policies: %d %s", res.Status, res.String())
	}
	plan(catalog.PIIRedaction, catalog.FirewallPolicies)
	settings["policies"] = []any{map[string]any{"name": "bad", "when": "true &&\n  request.nope", "action": "block"}}
	if er := put(settings).Error().Error; er.Code != handlers.CodeInvalidFirewall || len(er.Details) == 0 ||
		er.Details[0].Field != "policies[0].when" || !strings.HasPrefix(er.Details[0].Issue, "line 2,") {
		t.Fatalf("a policy that doesn't compile: %+v", er)
	}
	settings["policies"] = []any{policy}
	got := testclient.Decoded[handlers.FirewallSettings](put(settings).AssertStatus(http.StatusOK))
	if got.PII != "tokenize" || len(got.Policies) != 1 || got.CustomAction != "alert" {
		t.Fatalf("saved %+v", got)
	}

	// A downgrade keeps what is saved: it may be kept while the rest
	// changes, or removed, but not added to.
	plan()
	settings["scan_responses"] = false
	put(settings).AssertStatus(http.StatusOK)
	settings["custom"] = map[string]string{"badge": `BADGE-\d{6}`}
	if res := put(settings); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Free adds a pattern: %d %s", res.Status, res.String())
	}
	delete(settings, "custom")

	// The dry-run: secrets blocked, personal data tokenized, the policy on
	// where the call goes.
	try := func(content string, extra map[string]any) handlers.TestFirewallOut {
		t.Helper()
		in := map[string]any{"request": map[string]any{"model": "gpt-5-mini", "messages": []any{
			map[string]any{"role": "user", "content": content}}}}
		for k, v := range extra {
			in[k] = v
		}
		return testclient.Decoded[handlers.TestFirewallOut](e.client.Post(path+"/test", alice, testclient.JSON(in)).
			AssertStatus(http.StatusOK))
	}
	out := try("deploy with AKIAIOSFODNN7EXAMPLE", nil)
	if !out.Blocked || out.BlockedBy != "secret" || !strings.Contains(out.Message, "aws_access_key") || string(out.Request) != "null" ||
		len(out.Hits) != 1 || out.Hits[0].Location != "messages" {
		t.Fatalf("a secret %+v", out)
	}
	out = try("mail ada@example.com", nil)
	if out.Blocked || out.Action != "tokenize" || !strings.Contains(string(out.Request), "⟦email_1⟧") || out.Target.Model != "" {
		t.Fatalf("no provider yet %+v %s", out, out.Request)
	}
	e.client.Post(base+"/providers", alice, testclient.JSON(map[string]any{
		"name": "openai", "kind": "openai", "api_key": "sk-test-secret-abcd",
	})).AssertStatus(http.StatusCreated)
	if out = try("mail ada@example.com", nil); !out.Blocked || out.Policy != "no pii to openai" || out.Target.Kind != "openai" {
		t.Fatalf("the policy %+v", out)
	}
	// Settings tried before saving, a plan's features included.
	out = try("badge BADGE-123456", map[string]any{"settings": map[string]any{"enabled": true,
		"custom": map[string]string{"badge": `BADGE-\d{6}`}, "custom_action": "redact"}})
	if out.Action != "redact" || !strings.Contains(string(out.Request), "[REDACTED:badge]") {
		t.Fatalf("unsaved settings %+v", out)
	}
	if res := e.client.Post(path+"/test", alice, testclient.JSON(map[string]any{"request": map[string]any{"messages": []any{}}})); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("no model: %d %s", res.Status, res.String())
	}

	// Events and stats, without a request log here: empty, not failing.
	list := testclient.Decoded[httpx.List[handlers.FirewallEventOut]](e.client.Get(path+"/events?blocked=true", alice).
		AssertStatus(http.StatusOK))
	if len(list.Data) != 0 || list.NextCursor != "" {
		t.Fatalf("events %+v", list)
	}
	if res := e.client.Get(path+"/events?cursor=nope", alice); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a bad cursor: %d %s", res.Status, res.String())
	}
	st := testclient.Decoded[handlers.FirewallStatsOut](e.client.Get(path+"/stats", alice).AssertStatus(http.StatusOK))
	if st.To.Sub(st.From) != 7*24*time.Hour || st.Detectors == nil || st.Days == nil {
		t.Fatalf("stats %+v", st)
	}
	if res := e.client.Get(path+"/stats?from=2026-01-01T00:00:00Z&to=2026-06-01T00:00:00Z", alice); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("five months of stats: %d %s", res.Status, res.String())
	}
}

func TestModelsAndPrices(t *testing.T) {
	e := setup(t)
	alice := e.as("alice")
	_, _, base := e.project("alice")
	for _, p := range []map[string]any{
		{"name": "openai", "kind": "openai", "api_key": "sk-test-secret-abcd"},
		{"name": "router", "kind": "openrouter", "api_key": "sk-or-test-0000000000"},
	} {
		e.client.Post(base+"/providers", alice, testclient.JSON(p)).AssertStatus(http.StatusCreated)
	}
	models := testclient.Decoded[handlers.ModelsOut](e.client.Get(base+"/models", alice).AssertStatus(http.StatusOK))
	var mini *handlers.ModelOut
	for i := range models.Data {
		if models.Data[i].ID == "gpt-5-mini" {
			mini = &models.Data[i]
		}
	}
	if mini == nil || mini.Providers[0] != "openai" || mini.APIFormat != "openai" || mini.ListPrice == nil || mini.Price != nil ||
		len(models.OpenProviders) != 1 || models.OpenProviders[0].Provider != "router" {
		t.Fatalf("models %d, gpt-5-mini %+v, open %+v", len(models.Data), mini, models.OpenProviders)
	}
	one := testclient.Decoded[handlers.ModelsOut](e.client.Get(base+"/models?q=GPT-5-MINI", alice).AssertStatus(http.StatusOK))
	for _, m := range one.Data {
		if !strings.Contains(m.ID, "gpt-5-mini") {
			t.Fatalf("q kept %s", m.ID)
		}
	}
	if none := testclient.Decoded[handlers.ModelsOut](e.client.Get(base+"/models?api_format=anthropic", alice)); len(none.Data) != 0 ||
		len(none.OpenProviders) != 0 {
		t.Fatalf("anthropic %+v", none)
	}

	// The project's own price shows on the model, and the list's beside it.
	price := testclient.Decoded[handlers.PriceOut](e.client.Put(base+"/prices", alice, testclient.JSON(map[string]any{
		"model": "gpt-5-mini", "provider": "openai", "input": 0.1, "output": 0.4,
	})).AssertStatus(http.StatusOK))
	if price.ListPrice == nil || price.ListPrice.Input == 0.1 {
		t.Fatalf("price %+v", price)
	}
	if one = testclient.Decoded[handlers.ModelsOut](e.client.Get(base+"/models?q=gpt-5-mini", alice)); one.Data[0].Price == nil ||
		one.Data[0].Price.Output != 0.4 {
		t.Fatalf("own price %+v", one.Data[0])
	}
	list := testclient.Decoded[httpx.List[handlers.PriceOut]](e.client.Get(base+"/prices", alice).AssertStatus(http.StatusOK))
	if len(list.Data) != 1 || list.Data[0].ListPrice == nil {
		t.Fatalf("prices %+v", list)
	}
	// No request log here: nothing is missing.
	missing := testclient.Decoded[httpx.List[handlers.MissingPriceOut]](e.client.Get(base+"/prices/missing", alice).AssertStatus(http.StatusOK))
	if len(missing.Data) != 0 {
		t.Fatalf("missing %+v", missing)
	}
}

// Without the plan, the cache doctor says what it found and what fixing it
// is worth, not how.
func TestCacheDoctorTeaser(t *testing.T) {
	e := setupWith(t, true)
	ctx := context.Background()
	alice := e.as("alice")
	org, prj, base := e.project("alice")
	err := e.c.Events.Unscoped().Exec(ctx, `INSERT INTO requests (project_id, ts, trace, family_id, prefix_hash, prefix_flags,
		prefix_tokens, kind, served_model, usage_known, input_tokens, cache_read_tokens, retention_days)
		SELECT ?, now64(3) - toIntervalSecond(number), toString(number), 'dated', concat('p', toString(number)), ?, 3000,
		'anthropic', 'claude-sonnet-5-5', true, 3050, 0, 7 FROM numbers(100)`, mustID(t, ids.Project, prj.ID),
		uint16(families.Dates|families.Breakpoints))
	if err != nil {
		t.Fatal(err)
	}
	locked := testclient.Decoded[httpx.List[handlers.DiagnosisOut]](e.client.Get(base+"/cache-doctor", alice).AssertStatus(http.StatusOK))
	if len(locked.Data) != 1 || !locked.Data[0].Locked || locked.Data[0].PotentialMonthlyMicros == 0 ||
		locked.Data[0].Findings[0].Title == "" || locked.Data[0].Findings[0].Fix != "" || locked.Data[0].Findings[0].Detail != "" {
		t.Fatalf("Free %+v", locked.Data)
	}
	e.plan(t, org.ID, catalog.PromptCaching)
	open := testclient.Decoded[httpx.List[handlers.DiagnosisOut]](e.client.Get(base+"/cache-doctor", alice).AssertStatus(http.StatusOK))
	if open.Data[0].Locked || open.Data[0].Findings[0].Fix == "" {
		t.Fatalf("Pro %+v", open.Data)
	}
}

func TestLedgerExport(t *testing.T) {
	e := setupWith(t, true)
	ctx := context.Background()
	alice := e.as("alice")
	org, prj, base := e.project("alice")
	err := e.c.Events.Unscoped().Exec(ctx, `INSERT INTO requests (project_id, ts, trace, developer, route, requested_model,
		served_model, provider, status, usage_known, input_tokens, output_tokens, cost_micros, savings_micros, savings_reason,
		retention_days) SELECT ?, now64(3) - toIntervalSecond(number), concat('req_', toString(number)), '=cmd', 'default',
		'gpt-5', 'gpt-5-mini', 'openai', 200, true, 100, 10, 1500, 3500, 'routing', 7 FROM numbers(3)`,
		mustID(t, ids.Project, prj.ID))
	if err != nil {
		t.Fatal(err)
	}
	path := base + "/analytics/savings/export"
	if res := e.client.Get(path, alice); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Free: %d %s", res.Status, res.String())
	}
	e.plan(t, org.ID, catalog.LedgerExport)
	res := e.client.Get(path, alice).AssertStatus(http.StatusOK)
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") ||
		!strings.Contains(res.Header.Get("Content-Disposition"), prj.ID) {
		t.Fatalf("headers %v", res.Header)
	}
	lines, err := csv.NewReader(strings.NewReader(res.String())).ReadAll()
	if err != nil || len(lines) != 4 || lines[0][0] != "time" || lines[1][1] != "req_0" || lines[1][3] != "'=cmd" ||
		lines[1][16] != "0.001500" || lines[1][17] != "0.005000" || lines[1][18] != "0.003500" || lines[1][19] != "routing" {
		t.Fatalf("csv %v %v", lines, err)
	}
	if res := e.client.Get(path+"?from=2026-01-01T00:00:00Z&to=2026-03-01T00:00:00Z", alice); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("two months: %d %s", res.Status, res.String())
	}
}

func TestBudgets(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	alice := e.as("alice")
	org, prj, base := e.project("alice")
	orgID, prjID := mustID(t, ids.Org, org.ID), mustID(t, ids.Project, prj.ID)
	path := base + "/budgets"

	// Budgets come with Pro; Free is refused.
	res := e.client.Post(path, alice, testclient.JSON(map[string]any{
		"name": "All", "scope": "project", "window": "day", "limit_micros": 1_000_000, "channels": []string{},
	}))
	if res.Status != http.StatusPaymentRequired || res.Error().Error.Code != httpx.CodePlanRequired {
		t.Fatalf("Free: %d %s", res.Status, res.String())
	}
	e.c.DB.Ent.OrgFlag.Create().SetOrgID(orgID).SetKey("feature." + catalog.Budgets).SetValue(json.RawMessage(`true`)).
		SaveX(entx.SkipTenant(ctx))
	e.c.Tenancy.ForgetOrg(ctx, orgID)
	e.c.Billing.BustEntitlements(ctx, orgID)

	key := testclient.Decoded[platform.CreatedKeyOut](e.client.Post(base+"/keys", alice,
		testclient.JSON(map[string]any{"name": "app", "kind": "secret", "scopes": []string{core.ScopeGateway}})).
		AssertStatus(http.StatusCreated))
	block := testclient.Decoded[handlers.BudgetOut](e.client.Post(path, alice, testclient.JSON(map[string]any{
		"name": "Daily cap", "scope": "project", "window": "day", "limit_micros": 1_000_000, "action": "block",
		"thresholds": []int{100, 50, 50}, "channels": []string{},
	})).AssertStatus(http.StatusCreated))
	if block.Action != "block" || len(block.Thresholds) != 2 || block.Thresholds[0] != 50 || block.SpentMicros != 0 {
		t.Fatalf("created %+v", block)
	}
	perKey := testclient.Decoded[handlers.BudgetOut](e.client.Post(path, alice, testclient.JSON(map[string]any{
		"name": "App key", "scope": "key", "target": key.ID, "window": "month", "limit_micros": 50_000_000, "channels": []string{},
	})).AssertStatus(http.StatusCreated))
	if perKey.Target != key.ID || perKey.Action != "alert" {
		t.Fatalf("created %+v", perKey)
	}
	for _, body := range []map[string]any{
		{"name": "x", "scope": "key", "target": "nope", "window": "day", "limit_micros": 1, "channels": []string{}},
		{"name": "x", "scope": "project", "target": "x", "window": "day", "limit_micros": 1, "channels": []string{}},
		{"name": "x", "scope": "developer", "window": "day", "limit_micros": 1, "channels": []string{}},
	} {
		if res := e.client.Post(path, alice, testclient.JSON(body)); res.Status != http.StatusUnprocessableEntity {
			t.Errorf("%v: %d %s", body, res.Status, res.String())
		}
	}

	// A developer's budget comes with per-developer keys (Team).
	dev := map[string]any{"name": "Ana", "scope": "developer", "target": "ana@acme.test", "window": "day", "limit_micros": 1,
		"channels": []string{}}
	if res := e.client.Post(path, alice, testclient.JSON(dev)); res.Status != http.StatusPaymentRequired {
		t.Fatalf("Pro sets a developer's budget: %d %s", res.Status, res.String())
	}

	// Spend counted by the gateway shows in the budgets and alerts once.
	keyID := mustID(t, ids.APIKey, key.ID)
	e.app.Spend.Add(budgets.Who{Project: prjID, Key: keyID}, 1_500_000)
	if err := e.app.SpendCounters.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got := testclient.Decoded[handlers.BudgetOut](e.client.Get(path+"/"+block.ID, alice).AssertStatus(http.StatusOK))
	if got.SpentMicros != 1_500_000 || got.Percent != 150 {
		t.Fatalf("status %+v", got)
	}
	if err := e.app.Budgets.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.app.Budgets.Check(ctx); err != nil {
		t.Fatal(err)
	}
	var alerted int
	if err := e.c.DB.SQL.QueryRow(`SELECT alerted_percent FROM budgets WHERE name = 'Daily cap'`).Scan(&alerted); err != nil || alerted != 100 {
		t.Fatalf("alerted %d, %v", alerted, err)
	}
	// The project's activity has each alert, and the budget's history.
	history := testclient.Decoded[httpx.List[handlers.ActivityOut]](e.client.Get(base+"/activity?subject="+block.ID, alice).
		AssertStatus(http.StatusOK))
	if len(history.Data) != 1 || history.Data[0].Kind != "budget.exceeded" || history.Data[0].Severity != "critical" ||
		history.Data[0].Data["threshold"] != float64(100) || !strings.HasPrefix(history.Data[0].ID, "act_") {
		t.Fatalf("history %+v", history.Data)
	}
	if res := e.client.Get(base+"/activity?cursor=nope", alice); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a bad cursor: %d %s", res.Status, res.String())
	}
	blocking, err := e.app.Budgets.Blockings(entx.WithOrg(ctx, orgID), prjID)
	if err != nil || len(blocking) != 1 {
		t.Fatalf("blocking %v, %v", blocking, err)
	}
	if b, _, err := e.app.Spend.Exceeded(ctx, budgets.Who{Project: prjID, Key: keyID}, blocking); err != nil || b == nil {
		t.Fatalf("the spent budget doesn't block: %v, %v", b, err)
	}

	e.client.Delete(path+"/"+block.ID, alice).AssertStatus(http.StatusNoContent)
	e.client.Get(path+"/"+block.ID, alice).AssertStatus(http.StatusNotFound)
}

func TestPrices(t *testing.T) {
	e := setup(t)
	alice := e.as("alice")
	_, _, base := e.project("alice")
	path := base + "/prices"
	p := testclient.Decoded[handlers.PriceOut](e.client.Put(path, alice, testclient.JSON(map[string]any{
		"model": "llama-3.3-70b-versatile", "provider": "groq", "input": 0.59, "output": 0.79,
	})).AssertStatus(http.StatusOK))
	if p.CachedInput != 0.59 || p.CacheWrite != 0.59 || p.Provider != "groq" {
		t.Fatalf("set %+v", p)
	}
	again := testclient.Decoded[handlers.PriceOut](e.client.Put(path, alice, testclient.JSON(map[string]any{
		"model": "llama-3.3-70b-versatile", "provider": "groq", "input": 0.5, "output": 0.7,
	})).AssertStatus(http.StatusOK))
	if again.ID != p.ID || again.Input != 0.5 {
		t.Fatalf("replaced %+v", again)
	}
	list := testclient.Decoded[httpx.List[handlers.PriceOut]](e.client.Get(path, alice).AssertStatus(http.StatusOK))
	if len(list.Data) != 1 {
		t.Fatalf("list %+v", list)
	}
	if res := e.client.Put(path, alice, testclient.JSON(map[string]any{"model": "x", "input": -1, "output": 1})); res.Status !=
		http.StatusUnprocessableEntity || res.Error().Error.Code != handlers.CodeInvalidPrice {
		t.Fatalf("a negative price: %d %s", res.Status, res.String())
	}
	e.client.Delete(path+"/"+p.ID, alice).AssertStatus(http.StatusNoContent)
	e.client.Delete(path+"/"+p.ID, alice).AssertStatus(http.StatusNotFound)
}

// TestEveryRouteIsDocumented holds every route to a category and a title
// in the API reference.
func TestEveryRouteIsDocumented(t *testing.T) {
	e := setup(t)
	doc := testclient.Decoded[struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}](e.client.Get("/openapi.json").AssertStatus(http.StatusOK))
	found := false
	for path, ops := range doc.Paths {
		found = found || strings.HasSuffix(path, "/providers")
		for method, op := range ops {
			if c, _ := op["x-category"].(string); c == "" {
				t.Errorf("%s %s has no category", strings.ToUpper(method), path)
			}
			if title, _ := op["x-title"].(string); title == "" {
				t.Errorf("%s %s has no title", strings.ToUpper(method), path)
			}
		}
	}
	if !found {
		t.Fatal("the provider routes are not in the document")
	}
}

// TestEveryOrgRouteIsIsolated runs the tenant isolation check over the
// whole API, the shared platform routes included.
func TestEveryOrgRouteIsIsolated(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	alice := e.as("alice")
	me := testclient.Decoded[platform.MeOut](e.client.Get("/v1/me", alice))
	org, prj, base := e.project("alice")
	inv := testclient.Decoded[platform.InvitationOut](e.client.Post("/v1/orgs/"+org.ID+"/invitations", alice,
		testclient.JSON(map[string]string{"email": "x@example.com", "role": "viewer"})))
	key := testclient.Decoded[platform.CreatedKeyOut](e.client.Post(base+"/keys", alice,
		testclient.JSON(map[string]any{"name": "app", "kind": "secret", "scopes": []string{core.ScopeGateway}})).
		AssertStatus(http.StatusCreated))
	ch := testclient.Decoded[platform.ChannelOut](e.client.Post("/v1/orgs/"+org.ID+"/channels", alice,
		testclient.JSON(map[string]string{"kind": "email", "name": "Ops", "target": "ops@example.com"})).AssertStatus(http.StatusCreated))
	// Production webhook validation resolves DNS; create the endpoint directly.
	orgID, err := ids.Decode(ids.Org, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	ep := e.c.DB.Ent.WebhookEndpoint.Create().SetOrgID(orgID).SetURL("https://hooks.example.com/x").SetSecret("whsec_dGVzdA==").SaveX(ctx)
	del, err := e.c.Webhooks.Test(ctx, orgID, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := testclient.Decoded[handlers.ProviderOut](e.client.Post(base+"/providers", alice, testclient.JSON(map[string]any{
		"name": "openai", "kind": "openai", "api_key": "sk-test-secret-abcd",
	})).AssertStatus(http.StatusCreated))

	rte := testclient.Decoded[handlers.RouteOut](e.client.Post(base+"/routes", alice, testclient.JSON(map[string]any{
		"name": "default", "fallbacks": []string{"gpt-5-mini"},
	})).AssertStatus(http.StatusCreated))
	for _, f := range []string{catalog.Budgets, catalog.RoutingRules} {
		e.c.DB.Ent.OrgFlag.Create().SetOrgID(orgID).SetKey("feature." + f).SetValue(json.RawMessage(`true`)).
			SaveX(entx.SkipTenant(ctx))
	}
	e.c.Billing.BustEntitlements(ctx, orgID)
	bdg := testclient.Decoded[handlers.BudgetOut](e.client.Post(base+"/budgets", alice, testclient.JSON(map[string]any{
		"name": "All", "scope": "project", "window": "month", "limit_micros": 1_000_000, "channels": []string{ch.ID},
	})).AssertStatus(http.StatusCreated))

	prc := testclient.Decoded[handlers.PriceOut](e.client.Put(base+"/prices", alice, testclient.JSON(map[string]any{
		"model": "llama-3.3-70b", "input": 1, "output": 1,
	})).AssertStatus(http.StatusOK))

	rul := testclient.Decoded[handlers.RuleOut](e.client.Post(base+"/rules", alice, testclient.JSON(map[string]any{
		"name": "short", "when": "request.input_tokens < 1500", "to_model": "gpt-5-mini",
	})).AssertStatus(http.StatusCreated))
	rtr := testclient.Decoded[handlers.RouterOut](e.client.Post(base+"/routers", alice, testclient.JSON(map[string]any{
		"name": "auto", "tiers": []map[string]any{{"name": "fast", "model": "gpt-5-mini"}},
	})).AssertStatus(http.StatusCreated))

	victim := map[string]string{
		"org": org.ID, "project": prj.ID, "user": me.User.ID, "invitation": inv.ID, "key": key.ID, "channel": ch.ID,
		"webhook": ids.Encode(ids.WebhookEndpoint, ep.ID), "delivery": ids.Encode(ids.WebhookDelivery, del.ID),
		"meter": "requests", "provider": p.ID, "route": rte.ID, "budget": bdg.ID,
		"request": ids.Encode("req", ids.New()), "price": prc.ID, "family": "0123456789abcdef",
		"rule": rul.ID, "router": rtr.ID,
	}
	e.client.Put(base+"/keys/"+key.ID+"/gateway", alice, testclient.JSON(map[string]any{"rpm": 10})).AssertStatus(http.StatusOK)
	mallory := e.as("mallory")
	testkit.AssertTenantIsolation(t, e.client, victim, mallory)

	// Mallory, in her own organization, names Acme's objects.
	ownOrg, ownPrj, _ := e.project("mallory")
	// Her plan has budgets and rules too, so the IDs are what is checked.
	ownOrgID := mustID(t, ids.Org, ownOrg.ID)
	for _, f := range []string{catalog.Budgets, catalog.RoutingRules} {
		e.c.DB.Ent.OrgFlag.Create().SetOrgID(ownOrgID).SetKey("feature." + f).SetValue(json.RawMessage(`true`)).
			SaveX(entx.SkipTenant(ctx))
	}
	e.c.Billing.BustEntitlements(ctx, ownOrgID)
	// A family's ID is its template's hash, the same in every project that
	// uses the template: not another tenant's to protect.
	testkit.AssertNoForeignIDs(t, e.client, map[string]string{"org": ownOrg.ID, "project": ownPrj.ID, "meter": "requests",
		"family": "0123456789abcdef"},
		victim, []string{"meter", "family"}, map[string]string{
			"PATCH /v1/orgs/{org}/members/{user}":                             `{"role":"viewer"}`,
			"PUT /v1/orgs/{org}/projects/{project}/providers/{provider}":      `{"name":"x","kind":"openai","api_key":"sk-x-0000000000"}`,
			"PUT /v1/orgs/{org}/projects/{project}/keys/{key}/gateway":        `{"rpm":1}`,
			"PUT /v1/orgs/{org}/projects/{project}/routes/{route}":            `{"name":"x","fallbacks":[]}`,
			"PUT /v1/orgs/{org}/projects/{project}/budgets/{budget}":          `{"name":"x","scope":"project","window":"day","limit_micros":1,"channels":[]}`,
			"PUT /v1/orgs/{org}/projects/{project}/rules/{rule}":              `{"name":"x","when":"true","to_model":"m"}`,
			"PUT /v1/orgs/{org}/projects/{project}/routers/{router}":          `{"name":"x","tiers":[{"name":"a","model":"m"}]}`,
			"POST /v1/orgs/{org}/projects/{project}/routers/{router}/explain": `{"request":{"model":"x","messages":[]}}`,
			"POST /v1/orgs/{org}/projects/{project}/routers/{router}/restore": `{"tier":"fast"}`,
		}, mallory)
}
