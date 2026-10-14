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
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
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
	// The same request, written differently: answered from the cache, once
	// the answer is stored (in the background), without calling upstream.
	w.Eventually(func() bool {
		upstream := len(w.Fake.Requests())
		status, h, _ := w.Call("/v1/chat/completions", `{"temperature":0,"user":"u1","model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`)
		return status == 200 && h.Get(brand.HeaderCache) == "hit" && len(w.Fake.Requests()) == upstream
	})
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
		Basis          string `ch:"savings_basis"`
		Cache          string `ch:"cache"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT cost_micros, counterfactual_micros,
		savings_micros, savings_reason, savings_basis, cache FROM requests WHERE {tenant} AND usage_known ORDER BY ts`); err != nil {
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
		miss.Basis != "provider" || miss.Cache != "miss" {
		t.Fatalf("a miss %+v", miss)
	}
	if hit.Cost != 0 || hit.Counterfactual != 70 || hit.Savings != 70 || hit.Reason != "response_cache" ||
		hit.Basis != "cache" || hit.Cache != "hit" {
		t.Fatalf("a hit %+v", hit)
	}
	if fallback.Cost != 228 || fallback.Counterfactual != 70 || fallback.Savings != -158 || fallback.Reason != "fallback" ||
		fallback.Basis != "estimate" {
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
	bases := map[string]int64{}
	for _, r := range report.ByBasis {
		bases[r.Key] = r.Savings
	}
	if bases["cache"] != 70 || bases["estimate"] != -158 || bases["provider"] <= 0 || bases[""] != 0 {
		t.Fatalf("by basis %+v", report.ByBasis)
	}
}

// A call a model without a price served has an unknown cost: it books no
// savings, where a cost of 0 used to claim the whole counterfactual.
func TestUnpricedCallsSaveNothing(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	ctx := context.Background()
	w.Provider("local", providers.FormatOpenAI, []string{"llama-local"})
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{
		Name: "default", Fallbacks: []string{"local/llama-local"}, Retries: 0,
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`
	w.Eventually(func() bool { // the route reached the gateway
		w.Fake.Fail(503)
		status, h, _ := w.Call("/v1/chat/completions", body)
		return status == 200 && h.Get(brand.HeaderProvider) == "local"
	})
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Priced         bool   `ch:"priced"`
		Cost           int64  `ch:"cost_micros"`
		Counterfactual int64  `ch:"counterfactual_micros"`
		Savings        int64  `ch:"savings_micros"`
		Reason         string `ch:"savings_reason"`
		Basis          string `ch:"savings_basis"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT priced, cost_micros, counterfactual_micros,
		savings_micros, savings_reason, savings_basis FROM requests WHERE {tenant} AND served_model = 'llama-local'`); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no call served by the unpriced model")
	}
	for _, r := range rows {
		if r.Priced || r.Cost != 0 || r.Counterfactual != 0 || r.Savings != 0 || r.Reason != "" || r.Basis != "" {
			t.Fatalf("an unpriced call %+v", r)
		}
	}
}

// A provider's own figure for a call is its cost (the price list's stays
// beside it), even for a model the list doesn't price; the row names the
// backend that served it and the model the gateway asked for.
func TestReportedCostWins(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true,
		Upstream: fakeupstream.Options{Backend: "Google AI Studio", Cost: 0.000206}})
	ctx := context.Background()
	w.Provider("local", providers.FormatOpenAI, []string{"llama-local"})
	for _, model := range []string{"gpt-5-mini", "llama-local"} {
		if status, _, out := w.Call("/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`); status != 200 {
			t.Fatalf("%s: %d %s", model, status, out)
		}
	}
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Target   string `ch:"target_model"`
		Backend  string `ch:"backend"`
		Priced   bool   `ch:"priced"`
		Cost     int64  `ch:"cost_micros"`
		Computed int64  `ch:"computed_micros"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT target_model, backend, priced, cost_micros, computed_micros
		FROM requests WHERE {tenant} ORDER BY ts`); err != nil || len(rows) != 2 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	listed, unlisted := rows[0], rows[1]
	if listed.Target != "gpt-5-mini" || listed.Backend != "Google AI Studio" || !listed.Priced || listed.Cost != 206 || listed.Computed <= 0 {
		t.Fatalf("a listed model %+v", listed)
	}
	if unlisted.Target != "llama-local" || !unlisted.Priced || unlisted.Cost != 206 || unlisted.Computed != 0 {
		t.Fatalf("an unlisted model %+v", unlisted)
	}
}

// A conversation's calls on a caching route say what they should have read
// from the cache and what they did, and the hourly rollup adds them up by
// model, backend and strategy.
func TestCacheHealth(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Upstream: fakeupstream.Options{Backend: "b1", Prompt: 2000}})
	ctx := context.Background()
	w.Grant(catalog.PromptCaching)
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	calls := []string{
		`{"model":"gpt-5-mini","messages":[{"role":"system","content":"You fix code."},{"role":"user","content":"fix the bug"}]}`,
		`{"model":"gpt-5-mini","messages":[{"role":"system","content":"You fix code."},{"role":"user","content":"fix the bug"},
			{"role":"assistant","content":"done"},{"role":"user","content":"and the tests?"}]}`,
		`{"model":"gpt-5-mini","messages":[{"role":"system","content":"You fix code."},{"role":"user","content":"fix the bug"},
			{"role":"assistant","content":"done"},{"role":"user","content":"and the tests?"},{"role":"assistant","content":"added"},
			{"role":"user","content":"thanks"}]}`,
	}
	w.Eventually(func() bool { // the caching route reached the gateway
		_, _, _ = w.Call("/v1/chat/completions", `{"model":"gpt-5-mini","messages":[{"role":"user","content":"warm up"}]}`)
		recs := w.Records()
		return len(recs) > 0 && recs[len(recs)-1].CacheStrategy != ""
	})
	for _, body := range calls {
		if status, _, out := w.Call("/v1/chat/completions", body); status != 200 {
			t.Fatalf("%d %s", status, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Strategy string `ch:"cache_strategy"`
		Expected uint32 `ch:"cache_expected_tokens"`
		Read     uint32 `ch:"cache_read_tokens"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT cache_strategy, cache_expected_tokens, cache_read_tokens
		FROM requests WHERE {tenant} AND conversation != '' ORDER BY ts`); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 3 {
		t.Fatalf("%d rows", len(rows))
	}
	conv := rows[len(rows)-3:]
	if conv[0].Expected != 0 || conv[1].Expected != 2000 || conv[2].Expected != 2000 || conv[1].Strategy != "auto" {
		t.Fatalf("the conversation's calls %+v", conv)
	}
	var health []struct {
		Backend  string `ch:"backend"`
		Calls    uint64 `ch:"calls"`
		Expected uint64 `ch:"expected_calls"`
		Hits     uint64 `ch:"hit_calls"`
		Tokens   uint64 `ch:"expected_tokens"`
		Read     uint64 `ch:"expected_read_tokens"`
	}
	if err := w.C.Events.Select(ctx, &health, events.Project(w.Project), `SELECT backend, sum(calls) AS calls,
		sum(expected_calls) AS expected_calls, sum(hit_calls) AS hit_calls, sum(expected_tokens) AS expected_tokens,
		sum(expected_read_tokens) AS expected_read_tokens FROM cache_health_hourly WHERE {tenant} AND target_model = 'gpt-5-mini'
		GROUP BY backend`); err != nil || len(health) != 1 {
		t.Fatalf("%d rollup rows, %v", len(health), err)
	}
	if h := health[0]; h.Backend != "b1" || h.Expected != 2 || h.Hits != 2 || h.Tokens != 4000 || h.Read != 2*fakeupstream.CachedTokens {
		t.Fatalf("the rollup %+v", h)
	}
}

// A call that names a model router is logged with its tier and the step
// that chose it, and its savings count against the router's default tier.
func TestRouterCalls(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	w.Grant(catalog.Routers)
	ctx := context.Background()
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Active,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini"}, {Name: "smart", Model: "gpt-5"}},
			Rules: []tiers.Rule{{When: "request.messages == 1", Tier: "fast"}}}}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		_, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
		return h.Get(brand.HeaderTier) == "fast"
	})
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Router         string  `ch:"router"`
		Tier           string  `ch:"tier"`
		Decided        string  `ch:"decided_tier"`
		Source         string  `ch:"tier_source"`
		Confidence     float32 `ch:"tier_confidence"`
		Shadow         bool    `ch:"shadow"`
		Cost           int64   `ch:"cost_micros"`
		Counterfactual int64   `ch:"counterfactual_micros"`
		Savings        int64   `ch:"savings_micros"`
		Reason         string  `ch:"savings_reason"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT router, tier, decided_tier, tier_source,
		tier_confidence, shadow, cost_micros, counterfactual_micros, savings_micros, savings_reason
		FROM requests WHERE {tenant} AND tier = 'fast' ORDER BY ts DESC LIMIT 1`); err != nil || len(rows) != 1 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	r := rows[0]
	if r.Router != "auto" || r.Decided != "fast" || r.Source != "rule" || r.Confidence != 1 || r.Shadow ||
		r.Cost != 52 || r.Counterfactual <= r.Cost || r.Savings != r.Counterfactual-r.Cost || r.Reason != "routing" {
		t.Fatalf("row %+v", r)
	}
}

// An agent's conversation is logged a turn at a time: each call says
// whether it starts its turn or continues it, which conversation it is,
// and how long after the conversation's previous call it started.
func TestTurnsAreLogged(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	ctx := context.Background()
	calls := []string{
		`{"model":"gpt-5-mini","messages":[{"role":"system","content":"You fix code."},{"role":"user","content":"fix the bug"}]}`,
		`{"model":"gpt-5-mini","messages":[{"role":"system","content":"You fix code."},{"role":"user","content":"fix the bug"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"package a"}]}`,
		`{"model":"gpt-5-mini","messages":[{"role":"system","content":"You fix code."},{"role":"user","content":"fix the bug"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"package a"},{"role":"assistant","content":"fixed"},
			{"role":"user","content":"now add a test"}]}`,
	}
	for _, body := range calls {
		if status, _, out := w.Call("/v1/chat/completions", body); status != 200 {
			t.Fatalf("%d %s", status, out)
		}
		time.Sleep(20 * time.Millisecond) // the calls' starts are apart
	}
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Conversation string `ch:"conversation"`
		Kind         string `ch:"turn_kind"`
		Humans       uint16 `ch:"human_turns"`
		Gap          uint32 `ch:"chain_gap_ms"`
		State        string `ch:"chain_state"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT conversation, turn_kind, human_turns,
		chain_gap_ms, chain_state FROM requests WHERE {tenant} ORDER BY ts`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d rows", len(rows))
	}
	first, loop, next := rows[0], rows[1], rows[2]
	if first.Kind != "start" || first.State != "start" || first.Humans != 1 || first.Conversation == "" {
		t.Fatalf("the first question %+v", first)
	}
	if loop.Kind != "continuation" || loop.State != "continued" || loop.Gap < 20 || loop.Conversation != first.Conversation {
		t.Fatalf("the tool loop %+v", loop)
	}
	if next.Kind != "start" || next.State != "continued" || next.Gap < 20 || next.Humans != 2 || next.Conversation != first.Conversation {
		t.Fatalf("the next question %+v", next)
	}
}
