package gateway

import (
	"bytes"
	"encoding/json"
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
// providers the call shouldn't go to. A strategy with a session
// (promptcache.Sessions) keeps the conversation on one of them
// (OpenRouter's X-Session-Id): worth it where several backends serve a
// model, each caching apart; with one, a session only splits its cache by
// conversation (OpenAI's models, measured 2026-10-14: no conversation read
// the instructions another had cached). Which helps is learned per model.
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

// How each provider kind keeps a conversation where its prompt is cached
// (none: the zero value).
const (
	_ = iota
	// affinityKey: OpenAI's prompt_cache_key in the body, from a
	// conversation's second human message (a single question's key would
	// only spread the load less).
	affinityKey
	// affinitySession: OpenRouter's X-Session-Id header, from the
	// conversation's first call, for a strategy with a session
	// (promptcache.Sessions): OpenRouter keeps a session on the backend
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
	withSessions  = []string{promptcache.Auto, promptcache.Markers, promptcache.WithSession(promptcache.Auto),
		promptcache.WithSession(promptcache.Markers)}
)

// CacheDialectOf is the dialect of a provider kind; anthropic is a custom
// endpoint's, a cloud's or a translated call's speaking Anthropic Messages
// (Claude on a cloud: breakpoints; Azure OpenAI takes OpenAI's
// prompt_cache_key; Gemini on Vertex and Bedrock's OpenAI-compatible
// models cache on their own).
func CacheDialectOf(kind string, anthropic bool) CacheDialect {
	switch {
	case kind == "anthropic" || (kind == "custom" || cloudKind(kind)) && anthropic:
		return CacheDialect{Strategies: markersOnly}
	case kind == "openai" || kind == "azure":
		return CacheDialect{Strategies: autoOnly, affinity: affinityKey}
	case kind == "openrouter":
		return CacheDialect{Strategies: withSessions, Avoids: true, affinity: affinitySession}
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

// sessionHeader passes on a session the client chose (its own header; one
// in the body, session_id, stays there) and otherwise, where the plan
// keeps conversations on one backend (ours), sets OpenRouter's
// X-Session-Id on a call of a conversation (session), so the conversation
// stays on one backend from its first call. It reports a session the
// gateway set itself.
func sessionHeader(h http.Header, client http.Header, body []byte, kind, session string, ours bool) bool {
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
	if !ours || session == "" {
		return false
	}
	h.Set("X-Session-Id", session)
	return true
}

// withAvoided keeps a call to a router of providers off the backends its
// model's profile says never cache it (avoid, their slugs): OpenRouter's
// provider.ignore, appended, so the prompt's bytes don't move. Never
// provider.order, which would end OpenRouter's keeping a conversation on
// one backend; never over the client's own provider preferences. It
// reports whether it added any.
func withAvoided(body []byte, avoid []string) ([]byte, bool) {
	if len(avoid) == 0 {
		return body, false
	}
	if _, set, err := wire.Field(body, "provider"); err != nil || set {
		return body, false
	}
	ignore, err := json.Marshal(struct {
		Ignore []string `json:"ignore"`
	}{avoid})
	if err != nil {
		return body, false
	}
	out, err := wire.SetField(body, "provider", ignore)
	if err != nil {
		return body, false
	}
	return out, true
}

// refusedAvoided reports OpenRouter's refusal of a call no backend is
// left to serve once the avoided ones are taken out.
func refusedAvoided(body []byte) bool {
	b := bytes.ToLower(body)
	return bytes.Contains(b, []byte("have been ignored")) || bytes.Contains(b, []byte("ignored providers")) ||
		bytes.Contains(b, []byte("no endpoints")) || bytes.Contains(b, []byte("no allowed providers"))
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
