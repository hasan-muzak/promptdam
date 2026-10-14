package routers_test

import (
	"context"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

// A shadow router's report says where its calls went, what following it
// would save, each tier's measured quality against the default tier, and
// its calls' cost and cache rate by the model that answered them, its
// fallbacks apart.
func TestReport(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Sample: func() float64 { return 1 }})
	ctx := context.Background()
	row, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Shadow,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini"}, {Name: "smart", Model: "gpt-5", Fallbacks: []string{"main/gpt-5-mini"}},
			{Name: "expert", Model: "gpt-5-pro"}}, Default: "smart",
			Rules: []tiers.Rule{{When: "request.messages == 1", Tier: "fast"}}}})
	if err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		_, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
		return h.Get(brand.HeaderTierSource) == "shadow:rule"
	})
	for range 4 {
		w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	}
	w.Fake.Fail(503, 503, 503) // the smart tier's own model fails: its fallback answers
	w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	// Judged calls: 90 of the fast tier's answers held up, 0 lost; the
	// expert tier was needed for 10 of 40.
	batch, err := w.C.Events.Unscoped().PrepareBatch(ctx, `INSERT INTO router_evals (project_id, ts, org_id, router, family_id,
		baseline_tier, candidate_tier, candidate_rank, verdict, check, candidate_micros, retention_days)`)
	if err != nil {
		t.Fatal(err)
	}
	add := func(n int, tier string, rank int8, verdict string) {
		for range n {
			if err := batch.Append(w.Project, time.Now(), w.Org, "auto", "fam1", "smart", tier, rank, verdict, "judge", int64(10),
				uint16(7)); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(80, "fast", -1, "tie")
	add(10, "fast", -1, "win")
	add(10, "expert", 1, "win")
	add(30, "expert", 1, "tie")
	add(3, "expert", 1, "")
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}

	price := func(model string) (llm.Model, bool) { return w.App.Prices.Current().Lookup("", model) }
	rep, err := routers.BuildReport(ctx, w.C.Events, w.Project, routers.Of(row), time.Now().Add(-time.Hour), time.Now().Add(time.Hour), price)
	if err != nil {
		t.Fatal(err)
	}
	fast, smart, expert := rep.Tiers[0], rep.Tiers[1], rep.Tiers[2]
	if rep.Calls < 5 || fast.Decided != rep.Calls || smart.Served != rep.Calls || rep.Sources["shadow:rule"] != rep.Calls {
		t.Fatalf("calls %+v", rep)
	}
	// The fast tier costs less: following the router would save.
	if rep.EstimatedMicros <= 0 || rep.EstimatedMicros >= rep.CostMicros || rep.Unpriced != 0 {
		t.Fatalf("cost %d, estimated %d, unpriced %d", rep.CostMicros, rep.EstimatedMicros, rep.Unpriced)
	}
	// The fake provider reads 40 of every 100 input tokens from its cache.
	if len(smart.Models) != 2 || smart.Models[0] != (routers.TierModel{Provider: "main", Model: "gpt-5", Calls: rep.Calls - 1,
		CostMicros: smart.Models[0].CostMicros, Input: smart.Models[0].Input, CacheRead: smart.Models[0].CacheRead}) ||
		!smart.Models[1].Fallback || smart.Models[1].Model != "gpt-5-mini" || smart.Models[1].Calls != 1 ||
		smart.CacheRate() != 0.4 || smart.Models[1].CacheRate() != 0.4 || smart.CostPerCall() <= 0 ||
		smart.Models[1].CostPerCall() >= smart.Models[0].CostPerCall() {
		t.Fatalf("smart's models %+v", smart.Models)
	}
	if fast.Rank != -1 || fast.Judged != 90 || fast.Quality != 1 || !fast.MeetsFloor || fast.Low < 0.95 {
		t.Fatalf("fast %+v", fast)
	}
	if expert.Rank != 1 || expert.Judged != 40 || expert.Unjudged != 3 || expert.Quality != 0.25 || expert.MeetsFloor {
		t.Fatalf("expert %+v", expert)
	}
	if !rep.Ready || rep.EvalMicros != 1330 || len(rep.Families) != 2 || rep.Families[0].Family != "fam1" {
		t.Fatalf("report %+v", rep)
	}
	day := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	if m := routers.Monthly(100, day, day.Add(24*time.Hour)); m != 3000 {
		t.Fatalf("monthly %d", m)
	}
}
