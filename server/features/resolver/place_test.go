package resolver

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/catalog"
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
		{ID: "gpt-5-mini", Provider: "openai", ContextWindow: 400_000, Features: []string{llm.FeatureTools}},
		{ID: "claude-opus-5-5", Provider: "anthropic", ContextWindow: 200_000,
			Features: []string{llm.FeatureTools, llm.FeatureImages, llm.FeatureJSONSchema}},
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
