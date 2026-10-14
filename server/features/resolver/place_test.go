package resolver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/conversation"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/rules"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

// placing is a project with an OpenAI and an Anthropic provider, a price
// list saying what their models take, and a router of three tiers.
func placing(t testing.TB, status string) (*Resolver, *snapshot.Snapshot, *routers.Router) {
	t.Helper()
	cat := &llm.Catalog{Models: []llm.Model{
		{ID: "gpt-5-nano", Provider: "openai", ContextWindow: 1_000, Features: []string{}},
		{ID: "gpt-5-mini", Provider: "openai", ContextWindow: 400_000, Features: []string{llm.FeatureTools, llm.FeatureReasoning}},
		{ID: "claude-opus-5-5", Provider: "anthropic", ContextWindow: 200_000,
			Features: []string{llm.FeatureTools, llm.FeatureImages, llm.FeatureJSONSchema, llm.FeatureReasoning}},
	}}
	r := New(Config{Prices: prices.Static{Catalog: cat}})
	snap := &snapshot.Snapshot{Providers: []snapshot.Opened{
		{Provider: snapshot.Provider{ID: uuid.New(), Name: "openai", Kind: "openai", Format: providers.FormatOpenAI}},
		{Provider: snapshot.Provider{ID: uuid.New(), Name: "anthropic", Kind: "anthropic", Format: providers.FormatAnthropic}},
	}}
	rt := &routers.Router{Name: "auto", Status: status, Spec: tiers.Spec{Tiers: []tiers.Tier{
		{Name: "fast", Model: "gpt-5-nano"}, {Name: "mid", Model: "gpt-5-mini", Fallbacks: []string{"anthropic/claude-opus-5-5"}},
		{Name: "top", Model: "claude-opus-5-5"},
	}, Rules: []tiers.Rule{{When: "request.messages == 1", Tier: "fast"}}}}
	if err := rt.Spec.Check(); err != nil {
		t.Fatal(err)
	}
	env, err := rules.NewEnv()
	if err != nil {
		t.Fatal(err)
	}
	r.cfg.Rules = env
	return r, snap, rt
}

func call(t *testing.T, body string) *gateway.Call {
	t.Helper()
	req, err := wire.ParseRequest(wire.OpenAIChat, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return &gateway.Call{Format: wire.OpenAIChat, Request: req, Body: []byte(body), BodyBytes: len(body)}
}

func TestPlaceFollowsNeeds(t *testing.T) {
	r, snap, rt := placing(t, tiers.Active)
	one := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`

	pl, err := r.Explain(snap, rt, "", call(t, one), &Meta{})
	if err != nil || pl.Served != 0 || pl.Choice.Source != tiers.SourceRule || pl.Choice.Moved {
		t.Fatalf("a short call: %+v, %v", pl, err)
	}
	// The served tier first, then the stronger ones' models and fallbacks,
	// each once.
	var got []string
	for _, tg := range pl.Targets {
		got = append(got, tg.Provider+"/"+tg.Model)
	}
	if strings.Join(got, " ") != "openai/gpt-5-nano openai/gpt-5-mini anthropic/claude-opus-5-5" || pl.Targets[2].Format != wire.AnthropicMessages {
		t.Fatalf("targets %v", got)
	}

	// The fast tier's model takes no tools: the next one that does serves.
	tools := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}]}`
	pl, _ = r.Explain(snap, rt, "", call(t, tools), &Meta{})
	if pl.Served != 1 || !pl.Choice.Moved || pl.Fits[0].Lacks != llm.FeatureTools || pl.Fits[1].Lacks != "" {
		t.Fatalf("tools: %+v", pl)
	}
	// Images: only the top tier's model reads them.
	images := `{"model":"auto","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`
	pl, _ = r.Explain(snap, rt, "mid", call(t, images), &Meta{})
	if pl.Served != 2 || pl.Choice.Source != tiers.SourceExplicit || pl.Fits[1].Lacks != llm.FeatureImages {
		t.Fatalf("images: %+v", pl)
	}
	// A call larger than the fast model's context.
	long := `{"model":"auto","messages":[{"role":"user","content":"` + strings.Repeat("word ", 1_000) + `"}]}`
	pl, _ = r.Explain(snap, rt, "", call(t, long), &Meta{})
	if pl.Served != 1 || pl.Fits[0].Lacks != "context" || pl.Needs.Tokens < 1_000 {
		t.Fatalf("context: %+v", pl)
	}
}

func TestPlaceInShadowMode(t *testing.T) {
	r, snap, rt := placing(t, tiers.Shadow)
	one := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	m := &Meta{}
	targets, h, err := r.place(context.Background(), snap, rt, "", true, call(t, one), m)
	if err != nil {
		t.Fatal(err)
	}
	// The router chose fast, but serves its default tier while it watches.
	if !m.Shadow || m.Decided != "fast" || m.Tier != "top" || targets[0].Model != "claude-opus-5-5" ||
		h.Get("X-Promptdam-Tier-Source") != "shadow:rule" || m.Baseline.Model != "claude-opus-5-5" {
		t.Fatalf("meta %+v, targets %+v, header %v", m, targets, h)
	}
	// A tier the client names is served all the same.
	m = &Meta{}
	if _, _, err := r.place(context.Background(), snap, rt, "fast", true, call(t, one), m); err != nil || m.Shadow || m.Tier != "fast" {
		t.Fatalf("explicit: %+v, %v", m, err)
	}
	if _, _, err := r.place(context.Background(), snap, rt, "huge", true, call(t, one), &Meta{}); err == nil || !strings.Contains(err.Error(), "unknown_tier") {
		t.Fatalf("an unknown tier: %v", err)
	}
}

// What placing a call in a tier adds to the gateway: rules, needs and
// targets, with no I/O.
func BenchmarkPlace(b *testing.B) {
	r, snap, rt := placing(b, tiers.Active)
	body := `{"model":"auto","messages":[{"role":"system","content":"` + strings.Repeat("You extract invoice fields. ", 200) +
		`"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}]}`
	req, _ := wire.ParseRequest(wire.OpenAIChat, []byte(body))
	c := &gateway.Call{Format: wire.OpenAIChat, Request: req, Body: []byte(body), BodyBytes: len(body)}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := r.place(context.Background(), snap, rt, "", true, c, &Meta{}); err != nil {
			b.Fatal(err)
		}
	}
}

// On a plan without model routers (Free, or after a downgrade), an active
// router only watches, as in shadow mode; without learned routing, its
// classifier places nothing.
func TestPlansGateActiveRouters(t *testing.T) {
	r, snap, rt := placing(t, tiers.Active)
	features := []string{}
	r.cfg.Entitlements = func(context.Context, uuid.UUID) (billing.Entitlements, error) {
		return billing.Entitlements{Features: features}, nil
	}
	one := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	m := &Meta{}
	if _, _, err := r.place(context.Background(), snap, rt, "", true, call(t, one), m); err != nil || !m.Shadow ||
		m.Decided != "fast" || m.Tier != "top" {
		t.Fatalf("without routers: %+v, %v", m, err)
	}
	features = []string{catalog.Routers}
	m = &Meta{}
	if _, _, err := r.place(context.Background(), snap, rt, "", true, call(t, one), m); err != nil || m.Shadow || m.Tier != "fast" {
		t.Fatalf("with routers: %+v, %v", m, err)
	}
	if rt.Status != tiers.Active {
		t.Fatal("the snapshot's router changed")
	}
}

// countingDecider answers tier, counting the questions.
type countingDecider struct {
	tier  string
	asked int
}

func (d *countingDecider) Decide(context.Context, Question) Decision {
	d.asked++
	return Decision{Tier: d.tier, Outcome: DecisionAnswered}
}

// A turn keeps its tier: the calls of an agent's tool loop are placed as
// the call that started the turn was, even where the rule that placed it
// no longer holds; what a call needs still moves it up; an edited router
// places the turn anew; and the decision model is never asked mid-turn.
func TestTurnsKeepTheirTier(t *testing.T) {
	r, snap, rt := placing(t, tiers.Active)
	r.cfg.Turns = conversation.New(nil, conversation.Options{})
	ctx := context.Background()
	scope := conversation.Scope{Project: uuid.New(), Router: rt.Name}
	meta := func(c *gateway.Call) *Meta {
		m := &Meta{Project: scope.Project}
		m.Turn, m.InTurn = conversation.KeysOf(scope, "", c.Request.Turn)
		return m
	}
	start := call(t, `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	m := meta(start)
	if _, _, err := r.place(ctx, snap, rt, "", true, start, m); err != nil || m.Tier != "fast" || m.TierSource != tiers.SourceRule {
		t.Fatalf("the turn's start: %+v %v", m, err)
	}
	loop := func(result string) *gateway.Call {
		return call(t, `{"model":"auto","messages":[{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"`+result+`"}]}`)
	}
	next := loop("ok")
	m = meta(next)
	if _, _, err := r.place(ctx, snap, rt, "", true, next, m); err != nil || m.Tier != "fast" || m.TierSource != tiers.SourceRule ||
		m.Sampled {
		t.Fatalf("the tool loop left its turn's tier: %+v %v", m, err)
	}
	// A tool result past the fast model's context: the call moves up.
	big := loop(strings.Repeat("row ", 2_000))
	m = meta(big)
	if _, _, err := r.place(ctx, snap, rt, "", true, big, m); err != nil || m.Tier != "mid" {
		t.Fatalf("a call its tier can't take: %+v %v", m, err)
	}

	// The router changed: the turn is placed anew (the rule no longer
	// holds, so the default tier), and without asking the decision model
	// mid-turn.
	edited := *rt
	edited.Updated = edited.Updated.Add(time.Second)
	edited.DecisionModel = "gpt-5-nano"
	d := &countingDecider{tier: "mid"}
	r.cfg.Decider = d
	m = meta(next)
	if _, _, err := r.place(ctx, snap, &edited, "", true, next, m); err != nil || m.Tier != "top" || m.TierSource != tiers.SourceDefault ||
		d.asked != 0 {
		t.Fatalf("an edited router mid-turn: %+v %v, decision model asked %d times", m, err, d.asked)
	}
	// A new turn's start asks it.
	other := call(t, `{"model":"auto","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},
		{"role":"user","content":"is this contract fair?"}]}`)
	m = meta(other)
	if _, _, err := r.place(ctx, snap, &edited, "", true, other, m); err != nil || m.Tier != "mid" || d.asked != 1 {
		t.Fatalf("a new turn: %+v %v, asked %d", m, err, d.asked)
	}
}

// Within a tier, the target that served the turn's latest call goes first:
// its prompt cache is warm there.
func TestTurnsPreferTheirProvider(t *testing.T) {
	ts := []gateway.Target{{Provider: "a", Model: "m"}, {Provider: "b", Model: "m"}, {Provider: "c", Model: "m"}}
	preferFirst(ts, "c", "m")
	if ts[0].Provider != "c" || ts[1].Provider != "a" || ts[2].Provider != "b" {
		t.Fatalf("%+v", ts)
	}
	preferFirst(ts, "z", "m")
	if ts[0].Provider != "c" {
		t.Fatalf("an unknown provider moved %+v", ts)
	}
}

// Each tier's targets carry its reasoning effort, fitted to their model:
// OpenAI-compatible models whose levels the price list doesn't know take
// low to high.
func TestTargetsCarryTheirTiersEffort(t *testing.T) {
	r, snap, rt := placing(t, tiers.Active)
	rt.Spec.Tiers[1].Effort, rt.Spec.Tiers[2].Effort = "xhigh", "xhigh"
	two := `{"model":"auto","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	pl, err := r.Explain(snap, rt, "mid", call(t, two), &Meta{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tg := range pl.Targets {
		got[tg.Model] = tg.Effort
	}
	if got["gpt-5-mini"] != "high" || got["claude-opus-5-5"] != "xhigh" {
		t.Fatalf("efforts %v", got)
	}
}

// A new turn the cascade would put on a cheaper tier stays on its
// conversation's warm one when switching wouldn't pay for rebuilding the
// cache; a cold conversation switches.
func TestNewTurnsHoldAWarmTier(t *testing.T) {
	r, snap, rt := placing(t, tiers.Active)
	r.cfg.Prices = prices.Static{Catalog: &llm.Catalog{Models: []llm.Model{
		{ID: "gpt-5-nano", Provider: "openai", ContextWindow: 1_000, Features: []string{}, Input: 0.05, Output: 0.4},
		{ID: "gpt-5-mini", Provider: "openai", ContextWindow: 400_000, Features: []string{llm.FeatureTools, llm.FeatureReasoning},
			Input: 3.9, CachedInput: 0.39, Output: 19.5},
		{ID: "claude-opus-5-5", Provider: "anthropic", ContextWindow: 200_000, MinCacheTokens: 512,
			Features: []string{llm.FeatureTools, llm.FeatureImages, llm.FeatureJSONSchema, llm.FeatureReasoning},
			Input:    4, CachedInput: 0.4, CacheWrite: 5, Output: 20},
	}}}
	rt.Spec.Default = "mid" // no rule holds for a second turn: the cascade picks mid
	ctx := context.Background()
	scope := conversation.Scope{Project: uuid.New(), Router: rt.Name}
	second := call(t, `{"model":"auto","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},
		{"role":"user","content":"and the contract?"}]}`)
	place := func(lastCall time.Time) *Meta {
		t.Helper()
		r.cfg.Turns = conversation.New(nil, conversation.Options{})
		m := &Meta{Project: scope.Project}
		m.Turn, m.InTurn = conversation.KeysOf(scope, "", second.Request.Turn)
		// The first turn ran on the top tier, its prompt cached.
		r.cfg.Turns.Place(scope.Project, m.Turn.Prev, conversation.Placement{Tier: "top", Decided: "top",
			Source: tiers.SourceExplicit, Version: rt.Version()})
		r.cfg.Turns.Finish(ctx, scope.Project, m.Turn.Prev, conversation.Key{}, conversation.Finished{Start: lastCall.UnixMilli(),
			Input: 100_000, CacheRead: 90_000, CacheWrite: 10_000, Output: 2_000, Model: "claude-opus-5-5", Provider: "anthropic",
			Kind: "anthropic", TTL: "5m"})
		if _, _, err := r.place(ctx, snap, rt, "", true, second, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := place(time.Now().Add(-time.Minute)); m.Tier != "top" || m.TierSource != tiers.SourceHeld || m.Decided != "mid" {
		t.Fatalf("a warm conversation: tier %s (%s), decided %s", m.Tier, m.TierSource, m.Decided)
	}
	if m := place(time.Now().Add(-10 * time.Minute)); m.Tier != "mid" || m.TierSource != tiers.SourceDefault {
		t.Fatalf("a cold one: tier %s (%s)", m.Tier, m.TierSource)
	}
}
