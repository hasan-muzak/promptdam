package routers_test

import (
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

// A decision model picks a tier and writes no answers: a router may ask it
// which tier, never answer with it or judge with it.
func TestDecisionModelsDontAnswer(t *testing.T) {
	cat := &llm.Catalog{Models: []llm.Model{
		{ID: "typesafe/jev-1.13", Provider: "openrouter", Input: 0.042, Decisions: true},
		{ID: "deepseek/deepseek-v4.1-flash", Provider: "openrouter", Input: 0.02, Output: 0.891},
	}}
	s := routers.New(nil, nil, prices.Static{Catalog: cat}, nil)
	in := func(model, judge string) *routers.Input {
		return &routers.Input{Name: "advisor", Status: tiers.Shadow, Judge: judge, DecisionModel: "openrouter/typesafe/jev-1.13",
			Spec: tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: model}, {Name: "expert", Model: "openrouter/deepseek/deepseek-v4.1-flash"}}}}
	}
	if err := s.Check(in("openrouter/deepseek/deepseek-v4.1-flash", "")); err != nil {
		t.Fatalf("a decision model deciding: %v", err)
	}
	if err := s.Check(in("openrouter/typesafe/jev-1.13", "")); err == nil || !strings.Contains(err.Error(), "decision model") {
		t.Fatalf("a decision model answering: %v", err)
	}
	if err := s.Check(in("openrouter/deepseek/deepseek-v4.1-flash", "typesafe/jev-1.13")); err == nil {
		t.Fatal("a decision model judging")
	}
}
