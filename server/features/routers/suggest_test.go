package routers

import (
	"errors"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm"
)

func cand(provider, id string, in, out float64, features ...string) Candidate {
	m := llm.Model{ID: id, Input: in, Output: out}
	if features != nil {
		m.Features = features
	}
	return Candidate{Provider: provider, Kind: provider, Model: m}
}

func models(s Suggestion) string {
	var out []string
	for _, t := range s.Spec.Tiers {
		out = append(out, t.Name+"="+t.Model+"("+strings.Join(t.Fallbacks, ",")+")")
	}
	return strings.Join(out, " ")
}

func TestSuggest(t *testing.T) {
	anthropic := []Candidate{cand("claude", "claude-haiku-4-5", 1, 5), cand("claude", "claude-sonnet-4-5", 3, 15),
		cand("claude", "claude-sonnet-5-5", 3, 15), cand("claude", "claude-opus-5-5", 10, 50),
		cand("claude", "claude-sonnet-4-5-20250929", 3, 15)}
	openai := []Candidate{cand("openai", "gpt-5-mini", 0.5, 2), cand("openai", "gpt-5", 2, 10), cand("openai", "gpt-4o", 2.5, 10),
		cand("openai", "gpt-4o-mini", 0.2, 0.6), cand("openai", "text-embedding-3-small", 0.02, 0),
		cand("openai", "gpt-5.1", 2, 10), cand("openai", "gpt-5.1-nano", 0.05, 0.4)}

	// The first provider's ladder, the other's same tiers as fallbacks.
	s, err := Suggest(3, append(append([]Candidate{}, anthropic...), openai...), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := models(s); got != "fast=claude/claude-haiku-4-5(openai/gpt-5.1-nano) standard=claude/claude-sonnet-5-5(openai/gpt-5-mini) "+
		"expert=claude/claude-opus-5-5(openai/gpt-5.1)" {
		t.Fatalf("tiers %s", got)
	}
	if s.Spec.Default != "expert" || s.Judge != "claude/claude-sonnet-5-5" || s.Picks[0].How != "family" {
		t.Fatalf("suggestion %+v", s)
	}
	if err := s.Spec.Check(); err != nil {
		t.Fatalf("not a valid router: %v", err)
	}

	// Two tiers; OpenAI first.
	s, _ = Suggest(2, append(append([]Candidate{}, openai...), anthropic...), nil)
	if got := models(s); got != "fast=openai/gpt-5.1-nano(claude/claude-haiku-4-5) expert=openai/gpt-5.1(claude/claude-opus-5-5)" {
		t.Fatalf("two tiers %s", got)
	}

	// The model used most becomes the default tier: its own, when it is
	// none of the ladder's.
	cur := cand("openai", "gpt-4o", 2.5, 10)
	s, _ = Suggest(3, openai, &cur)
	if got := models(s); got != "fast=openai/gpt-5.1-nano() standard=openai/gpt-5-mini() expert=openai/gpt-5.1() current=openai/gpt-4o()" ||
		s.Spec.Default != "current" || len(s.Notes) < 2 {
		t.Fatalf("current: %s, default %s", got, s.Spec.Default)
	}
	cur = cand("claude", "claude-sonnet-5-5", 3, 15)
	if s, _ = Suggest(3, anthropic, &cur); s.Spec.Default != "standard" || len(s.Spec.Tiers) != 3 {
		t.Fatalf("current in the ladder: %s, default %s", models(s), s.Spec.Default)
	}

	// No known family: by price.
	custom := []Candidate{cand("local", "llama-8b", 0.05, 0.1), cand("local", "llama-70b", 0.6, 0.8), cand("local", "llama-405b", 3, 3),
		cand("local", "qwen-32b", 0.3, 0.4)}
	s, _ = Suggest(3, custom, nil)
	if got := models(s); got != "fast=local/qwen-32b() standard=local/llama-70b() expert=local/llama-405b()" || s.Picks[0].How != "price" {
		t.Fatalf("by price: %s", got)
	}

	// Models known to take no tools are left out; nothing usable is an
	// error.
	if _, err := Suggest(3, []Candidate{cand("x", "gpt-5", 2, 10, "images"), cand("x", "embed", 0.1, 0)}, nil); !errors.Is(err, ErrNoModels) {
		t.Fatalf("no usable model: %v", err)
	}
}

// A default tier whose model's efforts the price list knows gets a lighter
// sibling below it: the same model, one effort below its default.
func TestSuggestALighterTier(t *testing.T) {
	opus := cand("claude", "claude-opus-5-5", 4, 20)
	opus.Model.Efforts, opus.Model.DefaultEffort = []string{"low", "medium", "high", "xhigh", "max"}, "medium"
	s, err := Suggest(3, []Candidate{cand("claude", "claude-haiku-5-5", 0.1, 0.5), cand("claude", "claude-sonnet-5-5", 2, 10), opus}, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tier := range s.Spec.Tiers {
		names = append(names, tier.Name+"="+tier.Effort)
	}
	if got := strings.Join(names, " "); got != "fast= standard= expert-light=low expert=" || s.Spec.Default != "expert" {
		t.Fatalf("tiers %s, default %s", got, s.Spec.Default)
	}
	if light := s.Spec.Tiers[2]; light.Model != "claude/claude-opus-5-5" || s.Picks[len(s.Picks)-1].How != "effort" {
		t.Fatalf("the lighter tier %+v", light)
	}
	if err := s.Spec.Check(); err != nil {
		t.Fatalf("not a valid router: %v", err)
	}
}
