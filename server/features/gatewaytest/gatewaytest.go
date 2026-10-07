// Package gatewaytest runs the whole gateway against a fake provider for
// tests: keel's services on the test stack, an organization, a project and
// a gateway key, the resolver, snapshots and counters, and (with
// ClickHouse) the request log.
package gatewaytest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/apikeys"
	"github.com/hasan-muzak/keel-core/pkg/config"
	"github.com/hasan-muzak/keel-core/pkg/counter"
	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/identity"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/keel"
	"github.com/hasan-muzak/keel-core/pkg/safehttp"
	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/core"
	"github.com/hasan-muzak/promptdam/server/features/evaluate"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/responsecache"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
	"github.com/hasan-muzak/promptdam/server/migrations"
)

// Options shape a World.
type Options struct {
	// ClickHouse logs calls to a test ClickHouse database.
	ClickHouse bool
	// Upstream shapes the fake provider.
	Upstream fakeupstream.Options
	// Sample draws whether a router call is evaluated (the resolver's;
	// default random).
	Sample func() float64
}

// World is the gateway serving one project, with one provider ("main", a
// custom OpenAI-format endpoint at the fake provider) and one gateway key.
type World struct {
	T            *testing.T
	Ctx          context.Context
	C            *keel.Core
	App          *core.App
	Org, Project uuid.UUID
	Key          uuid.UUID
	// Raw is the key itself (pd_sk_…).
	Raw      string
	Fake     *fakeupstream.Server
	Upstream *httptest.Server
	Gateway  *httptest.Server
	Log      *requestlog.Log
	// Eval evaluates routers' sampled calls (with ClickHouse).
	Eval *evaluate.Evaluator

	mu      sync.Mutex
	records []gateway.Record
}

// New starts a World.
func New(t *testing.T, opts Options) *World {
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
	if opts.ClickHouse {
		c.Events = testkit.ClickHouse(t, migrations.ClickHouse())
	}
	w := &World{T: t, Ctx: ctx, C: c, App: core.NewApp(c)}
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
	w.Org, w.Project = org.ID, prj.ID
	rec, raw, err := c.APIKeys.Issue(ctx, apikeys.IssueInput{Org: org.ID, Project: &prj.ID, Name: "app", Kind: apikeys.Secret,
		Scopes: []string{core.ScopeGateway}})
	if err != nil {
		t.Fatal(err)
	}
	w.Key, w.Raw = rec.ID, raw

	w.Fake = fakeupstream.New(opts.Upstream)
	w.Upstream = httptest.NewServer(w.Fake)
	t.Cleanup(w.Upstream.Close)
	w.Provider("main", providers.FormatOpenAI, nil)

	snaps := snapshot.NewStore(c.KV, w.App.Sources())
	rates := counter.New(c.KV, "rpm", counter.Options{})
	w.Log = requestlog.New(requestlog.Config{
		Events: c.Events, Prices: w.App.Prices, Spend: w.App.Spend, Entitlements: c.Metering.Entitlements,
	})
	res := resolver.New(resolver.Config{
		Keys: c.APIKeys, Snapshots: snaps, Quota: c.Metering, Spend: w.App.Spend, Rates: rates, Prices: w.App.Prices,
		Scope: core.ScopeGateway, Rules: w.App.RuleEnv, Sample: opts.Sample,
	})
	transport := safehttp.NewTransport(safehttp.TransportOptions{AllowLoopback: true})
	evalSpend := counter.New(c.KV, "evalspend", counter.Options{})
	w.Eval = evaluate.New(evaluate.Config{Resolver: res, Transport: transport, Events: c.Events, Prices: w.App.Prices,
		Spend: w.App.Spend, Counters: evalSpend, Entitlements: c.Metering.Entitlements})
	for _, l := range []interface {
		Start(context.Context) error
		Stop(context.Context) error
	}{snaps, rates, w.App.SpendCounters, evalSpend, w.Log, w.Eval} {
		if err := l.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Stop(context.Background()) })
	}
	w.Gateway = httptest.NewServer(gateway.New(gateway.Config{
		Resolver: res, Transport: transport,
		Cache: responsecache.New(c.KV),
		Done: func(r *gateway.Record) {
			w.Log.Record(r)
			w.Eval.Offer(r)
			w.mu.Lock()
			defer w.mu.Unlock()
			w.records = append(w.records, *r)
		},
	}))
	t.Cleanup(w.Gateway.Close)
	return w
}

// Scoped is the test's context in the organization's tenant scope.
func (w *World) Scoped() context.Context { return entx.WithOrg(w.Ctx, w.Org) }

// Provider adds a custom provider speaking format at the fake provider.
func (w *World) Provider(name, format string, models []string) {
	w.T.Helper()
	key := "sk-" + name + "-0000000000"
	_, err := w.App.Providers.Create(w.Scoped(), w.Org, w.Project, providers.Input{
		Name: name, Kind: providers.Custom, Format: format, BaseURL: w.Upstream.URL + "/v1", APIKey: &key, Models: models,
		Enabled: true,
	})
	if err != nil {
		w.T.Fatal(err)
	}
}

// Serves makes a provider of the World serve only models: "main" serves
// every model until it lists its own.
func (w *World) Serves(name string, models ...string) {
	w.T.Helper()
	ps, err := w.App.Providers.List(w.Scoped(), w.Project)
	if err != nil {
		w.T.Fatal(err)
	}
	for _, p := range ps {
		if p.Name != name {
			continue
		}
		if _, err := w.App.Providers.Update(w.Scoped(), w.Org, w.Project, p.ID, providers.Input{Name: p.Name, Kind: p.Kind,
			Format: string(p.APIFormat), BaseURL: p.BaseURL, Models: models, Priority: p.Priority, Enabled: p.Enabled}); err != nil {
			w.T.Fatal(err)
		}
		return
	}
	w.T.Fatalf("no provider %s", name)
}

// Call sends a request with the World's key (GET for /v1/models, else
// POST) and returns the status, the headers and the body. header pairs
// set request headers.
func (w *World) Call(path, body string, header ...string) (int, http.Header, string) {
	w.T.Helper()
	method := http.MethodPost
	if path == "/v1/models" {
		method = http.MethodGet
	}
	req, _ := http.NewRequestWithContext(w.Ctx, method, w.Gateway.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+w.Raw)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.T.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return res.StatusCode, res.Header, string(out)
}

// Records returns the gateway's records so far.
func (w *World) Records() []gateway.Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]gateway.Record(nil), w.records...)
}

// Eventually retries check while a change reaches the gateway.
func (w *World) Eventually(check func() bool) {
	w.T.Helper()
	for range 300 {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.T.Fatal("the change never reached the gateway")
}
