package resolver

import (
	"slices"
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
// writes, on OpenRouter's own API; the provider's own caching for the
// others, and for an OpenRouter-kind endpoint of the project's own.
func TestTargetsDefaultCacheStrategy(t *testing.T) {
	cat := &llm.Catalog{Models: []llm.Model{
		{ID: "google/gemini-3.8-flash", Provider: "openrouter", Input: 0.75, CachedInput: 0.075, CacheWrite: 0.042, CacheWritePriced: true},
		{ID: "deepseek/deepseek-v4.1-flash", Provider: "openrouter", Input: 0.02, CachedInput: 0.005, CacheWrite: 0.02},
		{ID: "claude-haiku-5-5", Provider: "anthropic", Input: 0.1, CachedInput: 0.01, CacheWrite: 0.125},
		{ID: "gpt-5", Provider: "openai", Input: 1.25, CachedInput: 0.125},
	}}
	r := New(Config{Prices: prices.Static{Catalog: cat}})
	or := snapshot.Opened{Provider: snapshot.Provider{Name: "or", Kind: "openrouter", Format: providers.FormatOpenAI,
		BaseURL: "https://openrouter.ai/api/v1"}}
	proxy := or
	proxy.BaseURL = "https://llm-proxy.example.com/api/v1"
	for _, tt := range []struct {
		p     snapshot.Opened
		model string
		want  string
	}{
		{or, "google/gemini-3.8-flash", "markers"},
		{or, "deepseek/deepseek-v4.1-flash", "auto"},
		{proxy, "google/gemini-3.8-flash", "auto"},
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

// A cloud account calls each model in its family's API, by the cloud's
// own name for it: Claude in Anthropic's (Messages calls as they are, Chat
// calls translated), the others in the OpenAI-compatible one; and it
// serves the models of the vendors it hosts.
func TestCloudTargets(t *testing.T) {
	cat := &llm.Catalog{Models: []llm.Model{
		{ID: "claude-haiku-5-5", Provider: "anthropic", MaxOutput: 64_000, Features: []string{llm.FeatureReasoning}},
		{ID: "gemini-3.8-flash", Provider: "gcp.gemini", MaxOutput: 64_000},
		{ID: "gpt-6", Provider: "openai", MaxOutput: 128_000},
	}}
	r := New(Config{Prices: prices.Static{Catalog: cat}})
	cloud := func(kind, format string) snapshot.Opened {
		return snapshot.Opened{Provider: snapshot.Provider{Name: kind, Kind: kind, Format: format}}
	}
	bedrock, vertex, azure := cloud(providers.Bedrock, providers.FormatAnthropic), cloud(providers.Vertex, providers.FormatAnthropic),
		cloud(providers.Azure, providers.FormatOpenAI)
	for _, tt := range []struct {
		name   string
		p      snapshot.Opened
		f      wire.Format
		model  string
		want   string
		format wire.Format
	}{
		{"Claude on Bedrock, a Messages call", bedrock, wire.AnthropicMessages, "claude-haiku-5-5", "anthropic.claude-haiku-5-5", 0},
		{"Claude on Bedrock, a Chat call", bedrock, wire.OpenAIChat, "claude-haiku-5-5", "anthropic.claude-haiku-5-5", wire.AnthropicMessages},
		{"Bedrock's own id", bedrock, wire.AnthropicMessages, "anthropic.claude-haiku-5-5", "anthropic.claude-haiku-5-5", 0},
		{"Gemini on Vertex", vertex, wire.OpenAIChat, "gemini-3.8-flash", "google/gemini-3.8-flash", 0},
		{"Gemini on Vertex, a Messages call", vertex, wire.AnthropicMessages, "gemini-3.8-flash", "google/gemini-3.8-flash", wire.OpenAIChat},
		{"Claude on Vertex", vertex, wire.AnthropicMessages, "claude-haiku-5-5", "claude-haiku-5-5", 0},
		{"GPT on Azure", azure, wire.OpenAIChat, "gpt-6", "gpt-6", 0},
		{"Claude on Azure", azure, wire.AnthropicMessages, "claude-haiku-5-5", "claude-haiku-5-5", 0},
	} {
		tg, ok := r.target(tt.p, tt.f, tt.model)
		if !ok || tg.Model != tt.want || tg.Format != tt.format {
			t.Errorf("%s: %+v %v", tt.name, tg, ok)
		}
	}
	for _, tt := range []struct {
		p      snapshot.Opened
		model  string
		serves bool
	}{
		{bedrock, "claude-haiku-5-5", true}, {bedrock, "gpt-6", false}, {bedrock, "gemini-3.8-flash", false},
		{vertex, "gemini-3.8-flash", true}, {vertex, "claude-opus-9", true}, {vertex, "gpt-6", false},
		{azure, "gpt-6", true}, {azure, "claude-haiku-5-5", true}, {azure, "gemini-3.8-flash", false},
	} {
		if got := r.serves(tt.p.Provider, tt.model); got != tt.serves {
			t.Errorf("%s serves %s: %v", tt.p.Kind, tt.model, got)
		}
	}
	// The model list shows each cloud's vendors' models.
	models, open := r.ServedModels(&snapshot.Snapshot{Providers: []snapshot.Opened{vertex}}, providers.FormatOpenAI)
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"claude-haiku-5-5", "gemini-3.8-flash"}) || len(open) != 0 {
		t.Errorf("vertex lists %v (open %d)", ids, len(open))
	}
}
