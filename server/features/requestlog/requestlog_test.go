package requestlog_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/counter"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

func TestCost(t *testing.T) {
	prices := &llm.Catalog{Models: []llm.Model{
		{ID: "claude-x", Provider: "anthropic", Input: 3, CachedInput: 0.3, CacheWrite: 3.75, Output: 15},
		{ID: "gpt-x", Provider: "openai", Input: 1, CachedInput: 0.1, Output: 8},
	}}
	tests := []struct {
		name, kind, model string
		usage             wire.Usage
		want              int64
		ok                bool
	}{
		// 1M fresh input tokens at $1, 100k output at $8.
		{"plain", "openai", "gpt-x-2026-08-01", wire.Usage{Usage: llm.Usage{Input: 1_000_000, Output: 100_000}}, 1_800_000, true},
		// 400k of 1M input from the cache at $0.10.
		{"cache read", "openai", "gpt-x", wire.Usage{Usage: llm.Usage{Input: 1_000_000, CacheRead: 400_000}}, 640_000, true},
		// 100k cache writes: 60k for five minutes at $3.75, 40k for an hour at $6.
		{"one-hour writes", "anthropic", "claude-x",
			wire.Usage{Usage: llm.Usage{Input: 100_000, CacheWrite: 100_000}, CacheWrite1h: 40_000}, 465_000, true},
		{"custom finds any provider's model", providers.Custom, "claude-x",
			wire.Usage{Usage: llm.Usage{Input: 1_000_000}}, 3_000_000, true},
		{"unknown model", "openai", "nope", wire.Usage{Usage: llm.Usage{Input: 1}}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := requestlog.Cost(prices, tt.kind, tt.model, tt.usage)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("Cost = %d, %v; want %d, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// Calls through the gateway reach the request log and its rollups, and
// their cost counts against budgets.
func TestCallsAreLoggedAndCosted(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Upstream: fakeupstream.Options{Chunks: 4}})
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-haiku-4-5"})
	ctx := context.Background()

	calls := []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"gpt-5-mini","messages":[]}`},
		{"/v1/chat/completions", `{"model":"gpt-5-mini","stream":true,"messages":[]}`},
		{"/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":9,"messages":[]}`},
	}
	w.Eventually(func() bool {
		status, _, _ := w.Call(calls[2].path, calls[2].body, "X-Promptdam-Tags", "team:search, exp")
		return status == 200 // the new provider reached the snapshot
	})
	for _, c := range calls[:2] {
		if status, _, body := w.Call(c.path, c.body, "X-Promptdam-User", "user-7"); status != 200 {
			t.Fatalf("%s: %d %s", c.path, status, body)
		}
	}
	if err := w.Log.Stop(ctx); err != nil { // flushes the writer
		t.Fatal(err)
	}

	type sums struct {
		Requests uint64 `ch:"requests"`
		Cost     int64  `ch:"cost"`
		Input    uint64 `ch:"input"`
		Stream   uint64 `ch:"streamed"`
	}
	var raw []sums
	err := w.C.Events.Select(ctx, &raw, events.Project(w.Project),
		`SELECT count() AS requests, sum(cost_micros) AS cost, sum(input_tokens) AS input, countIf(stream) AS streamed
		 FROM requests WHERE {tenant}`)
	if err != nil {
		t.Fatal(err)
	}
	if raw[0].Requests != 3 || raw[0].Cost <= 0 || raw[0].Stream != 1 {
		t.Fatalf("raw %+v", raw[0])
	}
	var rolled []sums
	err = w.C.Events.Select(ctx, &rolled, events.Project(w.Project),
		`SELECT sum(requests) AS requests, sum(cost_micros) AS cost, sum(input_tokens) AS input, toUInt64(0) AS streamed
		 FROM requests_hourly WHERE {tenant}`)
	if err != nil {
		t.Fatal(err)
	}
	if rolled[0].Requests != 3 || rolled[0].Cost != raw[0].Cost || rolled[0].Input != raw[0].Input {
		t.Fatalf("rollup %+v, raw %+v", rolled[0], raw[0])
	}
	var tags []struct {
		Tag      string `ch:"tag"`
		Requests uint64 `ch:"requests"`
	}
	if err := w.C.Events.Select(ctx, &tags, events.Project(w.Project),
		`SELECT tag, sum(requests) AS requests FROM requests_hourly_tag WHERE {tenant} GROUP BY tag ORDER BY tag`); err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Tag != "exp" || tags[1].Tag != "team:search" {
		t.Fatalf("tags %+v", tags)
	}

	// The same cost counts against the project's budgets.
	spent, err := w.App.Spend.Spent(ctx, w.Project, budgets.ScopeProject, "", counter.Month)
	if err != nil || spent != raw[0].Cost {
		t.Fatalf("spent %d, %v; logged %d", spent, err, raw[0].Cost)
	}
	if w.Log.Dropped() != 0 {
		t.Fatalf("%d rows dropped", w.Log.Dropped())
	}
}

// A project's own price wins over the price list's.
func TestProjectPricesWin(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Upstream: fakeupstream.Options{Chunks: 4}})
	ctx := context.Background()
	// $1 per million for everything: 100 input tokens and 4 output tokens
	// cost 104 micro-USD.
	if _, err := w.App.PriceOverrides.Put(w.Scoped(), w.Org, w.Project, prices.Override{
		Model: "gpt-5-mini", Provider: "main", Input: 1, CachedInput: 1, CacheWrite: 1, Output: 1,
	}); err != nil {
		t.Fatal(err)
	}
	var trace string
	w.Eventually(func() bool {
		status, h, _ := w.Call("/v1/chat/completions", `{"model":"gpt-5-mini","messages":[]}`)
		trace = h.Get("X-Promptdam-Trace")
		records := w.Records()
		if status != 200 || len(records) == 0 {
			return false
		}
		m, _ := records[len(records)-1].Meta.(*resolver.Meta)
		return m != nil && m.Prices != nil
	})
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Cost int64 `ch:"cost_micros"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project),
		`SELECT cost_micros FROM requests WHERE {tenant} AND trace = ?`, trace); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Cost != 104 {
		t.Fatalf("cost %+v, want 104", rows)
	}
}

func TestProjectLookup(t *testing.T) {
	p := prices.ProjectPrices([]prices.Override{
		{Model: "llama-3.3-70b", Provider: "groq", Input: 0.5, Output: 0.8},
		{Model: "llama-3.3-70b", Input: 1, Output: 1},
	})
	if m, ok := p.Lookup("groq", "llama-3.3-70b"); !ok || m.Input != 0.5 {
		t.Fatalf("the provider's own price: %+v", m)
	}
	if m, ok := p.Lookup("together", "llama-3.3-70b"); !ok || m.Input != 1 {
		t.Fatalf("the project-wide price: %+v", m)
	}
	if _, ok := p.Lookup("groq", "gpt-5"); ok {
		t.Fatal("a model without a price of its own")
	}
	var none prices.Project
	if _, ok := none.Lookup("groq", "x"); ok {
		t.Fatal("no prices, no match")
	}
}

// The ledger weighs each call against the model asked for at list price,
// without any cache: with the fake provider's usage (100 input tokens, 40
// of them cached, 10 output) and the embedded prices, gpt-5-mini costs 52
// micro-USD against 70, and gpt-5 costs 228.
func TestSavingsLedger(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	ctx := context.Background()
	w.Provider("backup", providers.FormatOpenAI, []string{"gpt-5"})
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{
		Name: "default", Fallbacks: []string{"backup/gpt-5"}, Retries: 0, CacheTTL: 60,
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}],"temperature":0}`
	w.Eventually(func() bool { // the route reached the gateway: answers are cached
		_, h, _ := w.Call("/v1/chat/completions", body)
		return h.Get(brand.HeaderCache) == "miss"
	})
	upstream := len(w.Fake.Requests())
	// The same request, written differently: answered from the cache.
	status, h, _ := w.Call("/v1/chat/completions", `{"temperature":0,"user":"u1","model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`)
	if status != 200 || h.Get(brand.HeaderCache) != "hit" || len(w.Fake.Requests()) != upstream {
		t.Fatalf("%d, cache %q, %d upstream calls", status, h.Get(brand.HeaderCache), len(w.Fake.Requests()))
	}
	// Streams aren't cached.
	if _, h, _ := w.Call("/v1/chat/completions", strings.Replace(body, `"temperature"`, `"stream":true,"temperature"`, 1)); h.Get(brand.HeaderCache) != "" {
		t.Fatalf("a stream: cache %q", h.Get(brand.HeaderCache))
	}
	// The model asked for fails: the fallback answers, at a higher price.
	w.Fake.Fail(503)
	other := strings.Replace(body, `"hi"`, `"hello"`, 1)
	if status, h, _ := w.Call("/v1/chat/completions", other); status != 200 || h.Get(brand.HeaderProvider) != "backup" {
		t.Fatalf("%d via %s", status, h.Get(brand.HeaderProvider))
	}
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	var rows []struct {
		Cost           int64  `ch:"cost_micros"`
		Counterfactual int64  `ch:"counterfactual_micros"`
		Savings        int64  `ch:"savings_micros"`
		Reason         string `ch:"savings_reason"`
		Cache          string `ch:"cache"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT cost_micros, counterfactual_micros,
		savings_micros, savings_reason, cache FROM requests WHERE {tenant} AND usage_known ORDER BY ts`); err != nil {
		t.Fatal(err)
	}
	last := len(rows) - 1
	hit, fallback := rows[last-2], rows[last]
	miss := rows[0]
	for _, r := range rows { // calls before the route reached the gateway didn't cache
		if r.Cache == "miss" {
			miss = r
			break
		}
	}
	if miss.Cost != 52 || miss.Counterfactual != 70 || miss.Savings != 18 || miss.Reason != "prompt_cache" ||
		miss.Cache != "miss" {
		t.Fatalf("a miss %+v", miss)
	}
	if hit.Cost != 0 || hit.Counterfactual != 70 || hit.Savings != 70 || hit.Reason != "response_cache" || hit.Cache != "hit" {
		t.Fatalf("a hit %+v", hit)
	}
	if fallback.Cost != 228 || fallback.Counterfactual != 70 || fallback.Savings != -158 || fallback.Reason != "fallback" {
		t.Fatalf("a fallback %+v", fallback)
	}

	report, err := analytics.New(w.C.Events).Savings(ctx, w.Project, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]int64{}
	for _, r := range report.ByReason {
		reasons[r.Key] = r.Savings
	}
	if reasons["response_cache"] != 70 || reasons["fallback"] != -158 || report.Total.Requests != uint64(len(rows)) {
		t.Fatalf("report %+v", report)
	}
}
