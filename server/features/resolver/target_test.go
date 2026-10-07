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
