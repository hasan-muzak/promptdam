package routers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	muzak "muzak.dev/framework"
	"muzak.dev/framework/testclient"

	"github.com/hasan-muzak/keel/pkg/config"
	"github.com/hasan-muzak/keel/pkg/ids"
	"github.com/hasan-muzak/keel/pkg/jobs"
	"github.com/hasan-muzak/keel/pkg/keel"
	"github.com/hasan-muzak/keel/pkg/platform"
	"github.com/hasan-muzak/keel/pkg/testkit"

	"github.com/hasan-muzak/keel/products/promptdam/server/core"
	"github.com/hasan-muzak/keel/products/promptdam/server/handlers"
	"github.com/hasan-muzak/keel/products/promptdam/server/migrations"
	"github.com/hasan-muzak/keel/products/promptdam/server/routers"
)

type env struct {
	c      *keel.Core
	app    *core.App
	api    *muzak.App
	client *testclient.Client
	issuer *testkit.FirebaseIssuer
}

func setup(t *testing.T) *env {
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
	iss := testkit.NewFirebaseIssuer(t, "demo-keel")
	c.Verifier = iss.Verifier()
	a := c.API("test")
	app := core.NewApp(c)
	routers.Mount(a, app)
	return &env{c: c, app: app, api: a, client: testclient.New(t, a), issuer: iss}
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

	victim := map[string]string{
		"org": org.ID, "project": prj.ID, "user": me.User.ID, "invitation": inv.ID, "key": key.ID, "channel": ch.ID,
		"webhook": ids.Encode(ids.WebhookEndpoint, ep.ID), "delivery": ids.Encode(ids.WebhookDelivery, del.ID),
		"meter": "requests", "provider": p.ID,
	}
	mallory := e.as("mallory")
	testkit.AssertTenantIsolation(t, e.client, victim, mallory)

	// Mallory, in her own organization, names Acme's objects.
	ownOrg, ownPrj, _ := e.project("mallory")
	testkit.AssertNoForeignIDs(t, e.client, map[string]string{"org": ownOrg.ID, "project": ownPrj.ID, "meter": "requests"},
		victim, []string{"meter"}, map[string]string{
			"PATCH /v1/orgs/{org}/members/{user}":                        `{"role":"viewer"}`,
			"PUT /v1/orgs/{org}/projects/{project}/providers/{provider}": `{"name":"x","kind":"openai","api_key":"sk-x-0000000000"}`,
		}, mallory)
}
