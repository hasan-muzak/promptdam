package requestlog_test

import (
	"context"
	"testing"

	"github.com/hasan-muzak/keel/pkg/counter"
	"github.com/hasan-muzak/keel/pkg/events"
	"github.com/hasan-muzak/keel/pkg/llm"
	"github.com/hasan-muzak/keel/pkg/llm/wire"

	"github.com/hasan-muzak/keel/products/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/budgets"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/requestlog"
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
