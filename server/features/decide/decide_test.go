package decide_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/safehttp"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/decide"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

func question(base string, timeout time.Duration) resolver.Question {
	return resolver.Question{Router: "auto", Family: "f", Schema: true, Timeout: timeout,
		Target: gateway.Target{Provider: "openai", Kind: "openai", BaseURL: base, Model: "gpt-5-nano"},
		Tiers: []tiers.Tier{{Name: "fast", Description: "Counting and lists", Examples: []string{"How many?"}},
			{Name: "smart", Description: "Legal judgment"}},
		Instructions: "You read contracts.", Request: "How many times is price used?"}
}

func TestPrompt(t *testing.T) {
	system, user := decide.Prompt(question("http://x", time.Second))
	for _, want := range []string{"- fast: Counting and lists\n  Example: How many?", "- smart: Legal judgment", `{"tier": "<name>"}`} {
		if !strings.Contains(system, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, system)
		}
	}
	if !strings.Contains(user, "<instructions>\nYou read contracts.") || !strings.Contains(user, "How many times is price used?") {
		t.Fatalf("user %q", user)
	}
}

// The decision model is asked once per router, family and message within
// the cache's time, and not waited for past the timeout.
func TestDecide(t *testing.T) {
	var calls atomic.Int32
	var slow atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			ResponseFormat struct {
				JSONSchema struct {
					Schema struct {
						Properties struct {
							Tier struct {
								Enum []string `json:"enum"`
							} `json:"tier"`
						} `json:"properties"`
					} `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Join(req.ResponseFormat.JSONSchema.Schema.Properties.Tier.Enum, ",") != "fast,smart" {
			http.Error(w, "no schema", http.StatusBadRequest)
			return
		}
		if slow.Load() {
			time.Sleep(300 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"tier\": \"FAST\"}"}}],"usage":{"prompt_tokens":80,"completion_tokens":5}}`))
	}))
	defer srv.Close()
	d := decide.New(safehttp.NewTransport(safehttp.TransportOptions{AllowLoopback: true}))
	ctx := context.Background()

	tier, usage, err := d.Decide(ctx, question(srv.URL, time.Second))
	if err != nil || tier != "fast" || usage.Input != 80 {
		t.Fatalf("%q %+v %v", tier, usage, err)
	}
	if tier, usage, _ = d.Decide(ctx, question(srv.URL, time.Second)); tier != "fast" || usage.Input != 0 || calls.Load() != 1 {
		t.Fatalf("cached: %q %+v, %d calls", tier, usage, calls.Load())
	}
	slow.Store(true)
	q := question(srv.URL, 50*time.Millisecond)
	q.Request = "Another question"
	start := time.Now()
	if tier, _, err := d.Decide(ctx, q); tier != "" || err == nil || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("past the timeout: %q %v after %s", tier, err, time.Since(start))
	}
}

// An active router asks its decision model about the calls its classifier
// is unsure of (here, every call: it isn't trained yet), and the call
// counts what that cost.
func TestRoutersAskTheDecisionModel(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Sample: func() float64 { return 1 },
		Upstream: fakeupstream.Options{Reply: func(model string, body []byte) string {
			if strings.Contains(string(body), "You route requests") {
				return `{"tier": "fast"}`
			}
			return "Answered by " + model
		}}})
	ctx := context.Background()
	w.Grant(catalog.Routers, catalog.RoutingLearned)
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Active,
		DecisionModel: "gpt-5-mini", DecisionTimeoutMS: 2000,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini", Description: "Simple questions"},
			{Name: "smart", Model: "gpt-5"}}}}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		_, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
		return h.Get(brand.HeaderTierSource) == "decision" && h.Get(brand.HeaderTier) == "fast"
	})
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Source string `ch:"tier_source"`
		Micros int64  `ch:"router_micros"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT tier_source, router_micros FROM requests
		WHERE {tenant} AND tier_source = 'decision' ORDER BY ts LIMIT 1`); err != nil || len(rows) != 1 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	if rows[0].Micros <= 0 {
		t.Fatalf("the decision cost nothing: %+v", rows[0])
	}
}

// Projects never share answers, whatever their routers are called; and a
// decision model's redirect isn't followed (its key would go along).
func TestAnswersAreTheProjectsOwn(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"tier\": \"fast\"}"}}]}`))
	}))
	defer srv.Close()
	d := decide.New(safehttp.NewTransport(safehttp.TransportOptions{AllowLoopback: true}))
	ctx := context.Background()
	for _, project := range []uuid.UUID{uuid.New(), uuid.New(), uuid.Nil} {
		q := question(srv.URL, time.Second)
		q.Project = project
		if tier, _, err := d.Decide(ctx, q); err != nil || tier != "fast" {
			t.Fatalf("%q %v", tier, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("%d calls for 3 projects", calls.Load())
	}

	var followed atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer elsewhere.Close()
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer moved.Close()
	q := question(moved.URL, time.Second)
	q.Project = uuid.New()
	if tier, _, _ := d.Decide(ctx, q); tier != "" || followed.Load() {
		t.Fatalf("followed a redirect: %q", tier)
	}
}

// A call a firewall policy would keep from the decision model's provider
// isn't shown to it, and a call a policy decided isn't sampled.
func TestPoliciesComeBeforeTheDecisionModel(t *testing.T) {
	var asked atomic.Int32
	w := gatewaytest.New(t, gatewaytest.Options{Sample: func() float64 { return 0 },
		Upstream: fakeupstream.Options{Reply: func(model string, body []byte) string {
			if strings.Contains(string(body), "You route requests") {
				asked.Add(1)
				return `{"tier": "fast"}`
			}
			return "Answered by " + model
		}}})
	w.Grant(catalog.Routers, catalog.RoutingLearned)
	fw := firewall.Defaults()
	fw.PII = firewall.Alert
	fw.Policies = []firewall.Policy{{Name: "personal data stays on gpt-5", When: `classes.pii > 0`,
		Action: firewall.PolicyRouteTo, Model: "gpt-5"}}
	if _, err := w.App.Firewall.Put(w.Scoped(), w.Org, w.Project, fw); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Active,
		DecisionModel: "gpt-5-mini", DecisionTimeoutMS: 2000, SampleRate: 0.5,
		Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini"}, {Name: "smart", Model: "gpt-5"}}}}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		_, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
		return h.Get(brand.HeaderTierSource) == "decision"
	})
	before := asked.Load()
	status, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"mail ada@example.com"}]}`)
	recs := w.Records()
	m := recs[len(recs)-1].Meta.(*resolver.Meta)
	if status != 200 || asked.Load() != before || m.Decision.Target.Model != "" || h.Get(brand.HeaderServedModel) != "gpt-5" ||
		m.Firewall.Policy == "" || m.Sampled {
		t.Fatalf("asked %d→%d, served %s, meta %+v", before, asked.Load(), h.Get(brand.HeaderServedModel), m)
	}
}
