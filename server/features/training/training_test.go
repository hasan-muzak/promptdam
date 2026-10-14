package training_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/classifier"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

var (
	easy = []string{"How many times is the word %s used?", "List every %s in the text.", "Extract the %s from this invoice."}
	hard = []string{"Why is the %s clause unenforceable?", "Prove that the %s claim holds.", "Is this %s contract legal in Germany?"}
	noun = []string{"revenue", "date", "total", "party", "warranty", "term", "price", "address"}
)

// A router's judged calls train its classifier, and the gateway places
// calls with it: counting questions on the cheap tier, legal ones on the
// default.
func TestTrainedRoutersPlaceCalls(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Sample: func() float64 { return 1 }})
	ctx := context.Background()
	// Learned routing comes with Team.
	w.Grant(catalog.Routers, catalog.RoutingLearned)
	rt, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Active,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini"}, {Name: "smart", Model: "gpt-5"}}}})
	if err != nil {
		t.Fatal(err)
	}

	// 600 judged calls: the fast tier holds up on counting, listing and
	// extracting, not on proving or judging legality.
	// The default tier writes three times what the fast one does.
	batch, err := w.C.Events.Unscoped().PrepareBatch(ctx, `INSERT INTO router_evals (project_id, ts, org_id, router,
		baseline_tier, candidate_tier, candidate_rank, verdict, check, candidate_output_tokens, baseline_output_tokens,
		features, retention_days)`)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range 600 {
		ok := i%2 == 0
		tmpl := easy[rng.IntN(len(easy))]
		verdict := "tie"
		if !ok {
			tmpl, verdict = hard[rng.IntN(len(hard))], "loss"
		}
		text := fmt.Sprintf(tmpl, noun[rng.IntN(len(noun))])
		in := classifier.Input{Ask: text, Messages: 1, InputTokens: (len(`{"model":"auto","messages":[{"role":"user","content":""}]}`) + len(text)) / 4}
		if err := batch.Append(w.Project, time.Now(), w.Org, "auto", "smart", "fast", int8(-1), verdict, "judge",
			uint32(100), uint32(300), classifier.Features(in), uint16(7)); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}

	if err := w.App.Training.TrainDue(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := w.App.Ent.RouterClassifier.Query().Only(w.Scoped())
	if err != nil {
		t.Fatal(err)
	}
	if m := stored.Model; m.Version != 1 || m.Default != "smart" || len(m.Tiers) != 1 || m.Tiers[0].Threshold <= 0 ||
		stored.Samples != 600 || stored.RouterID != rt.ID || m.Tiers[0].OutputRatio != 3 || m.Tiers[0].OutputPairs != 600 {
		t.Fatalf("classifier %+v", stored)
	}
	// Nothing judged since: nothing to train.
	if err := w.App.Training.TrainDue(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := w.App.Ent.RouterClassifier.Query().Only(w.Scoped()); again.Model.Version != 1 {
		t.Fatalf("trained again: %+v", again.Model)
	}

	call := func(text string) (tier, source string) {
		_, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"`+text+`"}]}`)
		return h.Get(brand.HeaderTier), h.Get(brand.HeaderTierSource)
	}
	w.Eventually(func() bool {
		tier, source := call("How many times is the word price used?")
		return tier == "fast" && source == "classifier"
	})
	if tier, source := call("Is this warranty contract legal in Germany?"); tier != "smart" || source != "default" {
		t.Fatalf("a legal question: %s %s", tier, source)
	}

	// A call the classifier sent to the fast tier saved what the default
	// tier would have cost writing three times its output, measured.
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Output         uint32 `ch:"output_tokens"`
		Counterfactual int64  `ch:"counterfactual_micros"`
		Basis          string `ch:"savings_basis"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT output_tokens, counterfactual_micros,
		savings_basis FROM requests WHERE {tenant} AND tier = 'fast' AND tier_source = 'classifier' LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Basis != "replays" {
		t.Fatalf("a routed call's ledger %+v", rows)
	}
	gpt5, _ := llm.Default().Lookup("openai", "gpt-5")
	want := int64(gpt5.CostMicros(llm.Usage{Input: uint64(fakeupstream.PromptTokens), Output: 3 * uint64(rows[0].Output)}))
	if rows[0].Counterfactual != want {
		t.Fatalf("counterfactual %d, want %d (the default tier writing 3x)", rows[0].Counterfactual, want)
	}
}

// A judge that disagrees with itself too often when asked both ways has
// every verdict of its router judged both ways.
func TestFlippingJudgesAreAskedTwice(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	ctx := context.Background()
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Shadow,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini"}, {Name: "smart", Model: "gpt-5"}}}}); err != nil {
		t.Fatal(err)
	}
	batch, err := w.C.Events.Unscoped().PrepareBatch(ctx, `INSERT INTO router_evals (project_id, ts, org_id, router,
		baseline_tier, candidate_tier, candidate_rank, verdict, check, consistent, both_orders, retention_days)`)
	if err != nil {
		t.Fatal(err)
	}
	// 100 verdicts judged both ways, 20 of them inconsistent.
	for i := range 100 {
		if err := batch.Append(w.Project, time.Now(), w.Org, "auto", "smart", "fast", int8(-1), "tie", "judge", i >= 20, true,
			uint16(7)); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}
	if err := w.App.Training.TrainDue(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := w.App.Ent.RouterClassifier.Query().Only(w.Scoped())
	if err != nil {
		t.Fatal(err)
	}
	if m := stored.Model; !m.JudgeBoth || m.FlipRate != 0.2 || m.FlipSamples != 100 {
		t.Fatalf("classifier %+v", m)
	}
}
