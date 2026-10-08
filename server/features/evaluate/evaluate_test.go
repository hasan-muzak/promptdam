package evaluate_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/counter"
	"github.com/hasan-muzak/keel-core/pkg/events"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/evaluate"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

type row struct {
	Served, Decided, Baseline, Candidate string
	Model, Verdict, Check, Judge         string
	Rank                                 int8
	Consistent                           bool
	CandidateMicros, BaselineMicros      int64
	JudgeMicros                          int64
	Features                             []uint32
}

func rows(t *testing.T, w *gatewaytest.World) []row {
	t.Helper()
	var out []row
	err := w.C.Events.Select(context.Background(), &out, events.Project(w.Project), `SELECT served_tier AS Served,
		decided_tier AS Decided, baseline_tier AS Baseline, candidate_tier AS Candidate, candidate_model AS Model,
		verdict AS Verdict, check AS Check, judge_model AS Judge, candidate_rank AS Rank, consistent AS Consistent,
		candidate_micros AS CandidateMicros, baseline_micros AS BaselineMicros, judge_micros AS JudgeMicros,
		features AS Features FROM router_evals WHERE {tenant} ORDER BY ts`)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A sampled router call is replayed on the router's other tiers and each
// answer judged against the default tier's: the judge's verdict when the
// answers differ, a tie without one when they say the same.
func TestEvaluation(t *testing.T) {
	judged := make(chan string, 100)
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Sample: func() float64 { return 0 },
		Upstream: fakeupstream.Options{Reply: func(model string, body []byte) string {
			switch {
			case strings.Contains(string(body), "<answer_a>"):
				judged <- model
				return `{"better": "tie"}`
			case model == "claude-opus-5-5":
				return "The clause is enforceable, with two caveats."
			}
			return "It is enforceable."
		}}})
	// The judge (claude-sonnet-5-5 by default) is the Anthropic provider's.
	w.Serves("main", "gpt-5-mini")
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-opus-5-5", "claude-sonnet-5-5"})
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Shadow,
		EvalBudgetMicros: routers.DefaultBudgetMicros,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini"}, {Name: "smart", Model: "claude-opus-5-5"}},
			Rules: []tiers.Rule{{When: "request.messages == 1", Tier: "fast"}}}}); err != nil {
		t.Fatal(err)
	}
	// In shadow mode the default tier answers the client; the fast tier's
	// answer to the same request is judged against it.
	w.Eventually(func() bool {
		_, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","stream":true,"messages":[{"role":"user","content":"Is it enforceable?"}]}`)
		return h.Get(brand.HeaderTierSource) == "shadow:rule"
	})
	deadline := time.Now().Add(10 * time.Second)
	for w.Eval.Evaluated() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err := w.Eval.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := rows(t, w)
	if len(got) == 0 {
		t.Fatalf("no verdict (dropped %d)", w.Eval.Dropped())
	}
	r := got[0]
	if r.Served != "smart" || r.Decided != "fast" || r.Baseline != "smart" || r.Candidate != "fast" || r.Rank != -1 ||
		r.Model != "gpt-5-mini" || r.Verdict != evaluate.Tie || r.Check != evaluate.CheckJudge || r.Judge != "claude-sonnet-5-5" ||
		len(r.Features) == 0 {
		t.Fatalf("verdict %+v", r)
	}
	// The answer the client got (streamed) is the default tier's: only the
	// fast tier was called again, and the judge.
	if r.CandidateMicros <= 0 || r.BaselineMicros != 0 || r.JudgeMicros <= 0 {
		t.Fatalf("costs %+v", r)
	}
	if m := <-judged; m != "claude-sonnet-5-5" {
		t.Fatalf("judged by %s", m)
	}
	// What evaluation spent counts against the router's budget and the
	// project's spend.
	spent, err := counter.New(w.C.KV, "evalspend", counter.Options{}).Total(context.Background(), w.Project.String()+":auto",
		counter.Month, time.Now())
	if err != nil || spent != r.CandidateMicros+r.JudgeMicros {
		t.Fatalf("evaluation spend %d, %v", spent, err)
	}
	project, err := w.App.Spend.Total(context.Background(), w.Project, budgets.ScopeProject, "", counter.Month, time.Now())
	if err != nil || project < spent {
		t.Fatalf("project spend %d, %v", project, err)
	}
}

// A router whose judge no provider serves records the calls only a judge
// could decide, without a verdict; a spent budget stops evaluation.
func TestEvaluationWithoutJudge(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Sample: func() float64 { return 0 },
		Upstream: fakeupstream.Options{Reply: func(model string, _ []byte) string { return "Answered by " + model }}})
	w.Grant(catalog.Routers)
	w.Serves("main", "gpt-5-mini", "gpt-5")
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Active,
		EvalBudgetMicros: 1, Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini"}, {Name: "smart", Model: "gpt-5"}}}}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		status, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
		return status == http.StatusOK && h.Get(brand.HeaderRouter) == "auto"
	})
	deadline := time.Now().Add(10 * time.Second)
	for w.Eval.Evaluated() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// The budget ($0.000001) is spent: later samples are dropped.
	for range 3 {
		w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	}
	deadline = time.Now().Add(10 * time.Second)
	for w.Eval.Dropped() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err := w.Eval.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := rows(t, w)
	if len(got) != 1 || got[0].Verdict != "" || got[0].Check != evaluate.CheckNoJudge || got[0].CandidateMicros <= 0 ||
		w.Eval.Dropped() == 0 {
		t.Fatalf("verdicts %+v, dropped %d", got, w.Eval.Dropped())
	}
}
