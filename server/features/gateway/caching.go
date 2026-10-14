package gateway

import (
	"bytes"
	"net/http"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/promptcache"
)

// CachePlan is how a call to a target uses its provider's prompt cache:
// Strategy is promptcache.Markers (the gateway adds breakpoints, for models
// that cache what a request marks) or promptcache.Auto (the provider caches
// on its own); "" is the format's own default (breakpoints on Anthropic
// Messages calls). Trial marks a conversation trying the strategy the
// model's profile is learning about; Avoid are backends of a router of
// providers the call shouldn't go to.
type CachePlan struct {
	Strategy string
	Trial    bool
	Avoid    []string
}

// strategy is the plan's strategy for a call in format f.
func (p CachePlan) strategy(f wire.Format) string {
	if p.Strategy != "" {
		return p.Strategy
	}
	if f == wire.AnthropicMessages {
		return promptcache.Markers
	}
	return promptcache.Auto
}

// How each provider kind keeps a conversation where its prompt is cached.
const (
	affinityNone = iota
	// affinityKey: OpenAI's prompt_cache_key in the body, from a
	// conversation's second human message (a single question's key would
	// only spread the load less).
	affinityKey
	// affinitySession: OpenRouter's X-Session-Id header, from the
	// conversation's first call: OpenRouter keeps a session on the backend
	// that served it, where otherwise it sticks only after a cache hit.
	affinitySession
)

// CacheDialect is what a provider kind's prompt cache takes: the
// strategies a model of it may use, whether its calls can be kept off some
// of its backends (Avoids: a router of providers), and how a conversation
// stays on the machines that cached it. Kinds aren't models: the table is
// small and fixed, and what each model needs is learned (cacheprofile).
type CacheDialect struct {
	Strategies []string
	Avoids     bool
	affinity   int
}

var (
	markersOnly   = []string{promptcache.Markers}
	autoOnly      = []string{promptcache.Auto}
	autoOrMarkers = []string{promptcache.Auto, promptcache.Markers}
)

// CacheDialectOf is the dialect of a provider kind; anthropic is a custom
// endpoint's or a translated call's speaking Anthropic Messages.
func CacheDialectOf(kind string, anthropic bool) CacheDialect {
	switch {
	case kind == "anthropic" || kind == "custom" && anthropic:
		return CacheDialect{Strategies: markersOnly}
	case kind == "openai":
		return CacheDialect{Strategies: autoOnly, affinity: affinityKey}
	case kind == "openrouter":
		return CacheDialect{Strategies: autoOrMarkers, Avoids: true, affinity: affinitySession}
	case kind == "custom":
		return CacheDialect{Strategies: autoOrMarkers}
	}
	return CacheDialect{Strategies: autoOnly}
}

// DefaultCacheStrategy is a model's strategy before anything is learned:
// breakpoints where its kind takes only those, and on a router of
// providers when its price list charges for cache writes (the provider
// caches what a request marks); the provider's own caching elsewhere.
func DefaultCacheStrategy(kind string, anthropic, writePriced bool) string {
	d := CacheDialectOf(kind, anthropic)
	switch {
	case len(d.Strategies) == 1:
		return d.Strategies[0]
	case writePriced:
		return promptcache.Markers
	}
	return promptcache.Auto
}

// sessionHeader sets OpenRouter's X-Session-Id on a call of a conversation
// (session), so the conversation stays on one backend from its first call;
// a session the client chose (its own header, or session_id in the body)
// wins. It reports a session the gateway set itself.
func sessionHeader(h http.Header, client http.Header, body []byte, kind, session string) bool {
	if CacheDialectOf(kind, false).affinity != affinitySession {
		return false
	}
	if _, set, err := wire.Field(body, "session_id"); err != nil || set {
		return false
	}
	if own := client.Get("X-Session-Id"); own != "" {
		h.Set("X-Session-Id", own)
		return false
	}
	if session == "" {
		return false
	}
	h.Set("X-Session-Id", session)
	return true
}

// refusedCaching is what of the gateway's caching a provider's 400 names:
// refusedMarkers (its breakpoints, cache_control), refusedOther (OpenAI's
// prompt_cache_key, or a session), or nothing.
func refusedCaching(body []byte) int {
	b := bytes.ToLower(body)
	switch {
	case bytes.Contains(b, []byte("cache_control")):
		return refusedMarkers
	case bytes.Contains(b, []byte("prompt_cache_key")) || bytes.Contains(b, []byte("session")):
		return refusedOther
	}
	return refusedNothing
}

const (
	refusedNothing = iota
	refusedMarkers
	refusedOther
)

// killsMarkers reports whether a refusal of the gateway's breakpoints
// switches them off for the model: only where its kind may cache without
// them. Anthropic's API takes breakpoints, so its refusals are of a
// call's own (a fifth breakpoint, a lifetime out of order): the call is
// sent again unmarked, and the model keeps them.
func killsMarkers(kind string, anthropic bool) bool {
	return len(CacheDialectOf(kind, anthropic).Strategies) > 1
}
