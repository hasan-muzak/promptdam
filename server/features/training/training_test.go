package training_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/entx"

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
	w.C.DB.Ent.OrgFlag.Create().SetOrgID(w.Org).SetKey("feature." + catalog.RoutingLearned).SetValue(json.RawMessage(`true`)).
		SaveX(entx.SkipTenant(ctx))
	w.C.Billing.BustEntitlements(ctx, w.Org)
	rt, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Active,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini"}, {Name: "smart", Model: "gpt-5"}}}})
	if err != nil {
		t.Fatal(err)
	}

	// 600 judged calls: the fast tier holds up on counting, listing and
	// extracting, not on proving or judging legality.
	batch, err := w.C.Events.Unscoped().PrepareBatch(ctx, `INSERT INTO router_evals (project_id, ts, org_id, router,
		baseline_tier, candidate_tier, candidate_rank, verdict, check, features, retention_days)`)
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
		in := classifier.Input{LastUser: text, Messages: 1, InputTokens: (len(`{"model":"auto","messages":[{"role":"user","content":""}]}`) + len(text)) / 4}
		if err := batch.Append(w.Project, time.Now(), w.Org, "auto", "smart", "fast", int8(-1), verdict, "judge",
			classifier.Features(in), uint16(7)); err != nil {
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
		stored.Samples != 600 || stored.RouterID != rt.ID {
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
}
