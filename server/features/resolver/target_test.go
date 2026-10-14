package resolver

import (
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

// A provider of the other API takes Chat Completions and Messages calls,
// translated, knowing what its model takes; it takes no other call.
func TestTranslatedTargets(t *testing.T) {
	cat := &llm.Catalog{Models: []llm.Model{
		{ID: "claude-haiku-4-5", Provider: "anthropic", MaxOutput: 64_000, Features: []string{llm.FeatureTools, llm.FeatureReasoning}},
		{ID: "gpt-5-nano", Provider: "openai", MaxOutput: 16_000, Features: []string{}},
	}}
	r := New(Config{Prices: prices.Static{Catalog: cat}})
	anthropic := snapshot.Opened{Provider: snapshot.Provider{Name: "a", Kind: "anthropic", Format: providers.FormatAnthropic}}
	openai := snapshot.Opened{Provider: snapshot.Provider{Name: "o", Kind: "openai", Format: providers.FormatOpenAI}}

	tg, ok := r.target(anthropic, wire.OpenAIChat, "claude-haiku-4-5")
	if !ok || tg.Format != wire.AnthropicMessages || !tg.Reasoning || tg.MaxTokens != maxTranslatedTokens {
		t.Fatalf("chat → anthropic: %+v %v", tg, ok)
	}
	tg, ok = r.target(openai, wire.AnthropicMessages, "gpt-5-nano")
	if !ok || tg.Format != wire.OpenAIChat || tg.Reasoning || tg.MaxTokens != 16_000 {
		t.Fatalf("messages → openai: %+v %v", tg, ok)
	}
	if tg, ok = r.target(openai, wire.OpenAIEmbeddings, "text-embedding-3-small"); !ok || tg.Format != 0 {
		t.Fatalf("same API: %+v %v", tg, ok)
	}
	for _, f := range []wire.Format{wire.OpenAIEmbeddings, wire.OpenAIResponses} {
		if _, ok := r.target(anthropic, f, "claude-haiku-4-5"); ok {
			t.Fatalf("%s calls aren't translated", f)
		}
	}
	if _, ok := r.target(openai, wire.AnthropicCountTokens, "gpt-5-nano"); ok {
		t.Fatal("count_tokens calls aren't translated")
	}
}

// A target knows how its model thinks and whether it takes sampling: from
// the price list, else, for a model the list doesn't have yet, from the
// docs' table.
func TestTargetsKnowThinking(t *testing.T) {
	cat := &llm.Catalog{Models: []llm.Model{{ID: "claude-opus-5-5", Provider: "anthropic", AlwaysThinks: true, NoSampling: true}}}
	r := New(Config{Prices: prices.Static{Catalog: cat}})
	anthropic := snapshot.Opened{Provider: snapshot.Provider{Name: "a", Kind: "anthropic", Format: providers.FormatAnthropic}}
	if tg, _ := r.target(anthropic, wire.AnthropicMessages, "claude-opus-5-5"); !tg.AlwaysThinks || !tg.NoSampling || tg.ThinkingOff != "" {
		t.Fatalf("listed: %+v", tg)
	}
	if tg, _ := r.target(anthropic, wire.AnthropicMessages, "claude-haiku-5-5"); tg.ThinkingOff != "disabled" || !tg.NoSampling || tg.MinCache != 512 {
		t.Fatalf("unlisted: %+v", tg)
	}
	if tg, _ := r.target(anthropic, wire.AnthropicMessages, "claude-haiku-4-5"); tg.ThinkingOff != "" || tg.NoSampling {
		t.Fatalf("an older model: %+v", tg)
	}
}

// A target's cache strategy before anything is learned: breakpoints for
// Anthropic and for OpenRouter models whose price list charges for cache
// writes; the provider's own caching for the others.
func TestTargetsDefaultCacheStrategy(t *testing.T) {
	cat := &llm.Catalog{Models: []llm.Model{
		{ID: "google/gemini-3.8-flash", Provider: "openrouter", Input: 0.75, CachedInput: 0.075, CacheWrite: 0.042, CacheWritePriced: true},
		{ID: "deepseek/deepseek-v4.1-flash", Provider: "openrouter", Input: 0.02, CachedInput: 0.005, CacheWrite: 0.02},
		{ID: "claude-haiku-5-5", Provider: "anthropic", Input: 0.1, CachedInput: 0.01, CacheWrite: 0.125},
		{ID: "gpt-5", Provider: "openai", Input: 1.25, CachedInput: 0.125},
	}}
	r := New(Config{Prices: prices.Static{Catalog: cat}})
	or := snapshot.Opened{Provider: snapshot.Provider{Name: "or", Kind: "openrouter", Format: providers.FormatOpenAI}}
	for _, tt := range []struct {
		p     snapshot.Opened
		model string
		want  string
	}{
		{or, "google/gemini-3.8-flash", "markers"},
		{or, "deepseek/deepseek-v4.1-flash", "auto"},
		{snapshot.Opened{Provider: snapshot.Provider{Name: "a", Kind: "anthropic", Format: providers.FormatAnthropic}}, "claude-haiku-5-5", "markers"},
		{snapshot.Opened{Provider: snapshot.Provider{Name: "o", Kind: "openai", Format: providers.FormatOpenAI}}, "gpt-5", "auto"},
		{snapshot.Opened{Provider: snapshot.Provider{Name: "c", Kind: "custom", Format: providers.FormatAnthropic}}, "claude-x", "markers"},
		{snapshot.Opened{Provider: snapshot.Provider{Name: "c", Kind: "custom", Format: providers.FormatOpenAI}}, "llama", "auto"},
	} {
		if tg, _ := r.target(tt.p, wire.OpenAIChat, tt.model); tg.Cache.Strategy != tt.want {
			t.Errorf("%s %s: %q, want %q", tt.p.Kind, tt.model, tg.Cache.Strategy, tt.want)
		}
	}
}
