package resolver

import (
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/features/conversation"
)

// Switching a conversation to a cheaper tier's model rebuilds its prompt
// cache there: every cached token written again (at the write price)
// rather than read (at a tenth of the input price). A turn that starts on a
// cheaper tier than the conversation's warm one stays put unless the
// saving over the turns ahead pays for that rebuild.

// warmMargin is how close to its expiry a cache is taken as cold: the
// call has to reach the provider before the entry lapses.
const warmMargin = 15 * time.Second

// maxTurnsAhead bounds the turns a switch is expected to save on: a long
// conversation's next turns, never its whole future.
const maxTurnsAhead = 4

// defaultMinCache is the shortest prefix a cache keeps when the price
// list doesn't say a model's.
const defaultMinCache = 1024

// switchSide is a model a conversation is on, or could switch to: its
// prices, its provider's kind, how much it writes against the router's
// default tier (outputScale: 1 when not measured), and the share of what
// its calls should read from its cache that they don't (misses, measured
// across projects: cacheprofile; 0 until measured).
type switchSide struct {
	model       llm.Model
	kind        string
	outputScale float64
	misses      float64
}

// holdsWarm reports whether a conversation whose previous turn ran on cur
// (rec: its calls, tokens and cache state) should stay there rather than
// switch to next, at now, with humanTurns human messages so far. Unknown
// prices or a cold cache never hold.
func holdsWarm(cur, next switchSide, rec conversation.Record, humanTurns int, now time.Time) bool {
	if rec.Calls == 0 || rec.Cached <= 0 || cur.model.Input <= 0 || next.model.Input <= 0 || cur.model.Output <= 0 ||
		next.model.Output <= 0 {
		return false
	}
	minCache := int64(cur.model.MinCacheTokens)
	if minCache == 0 {
		minCache = defaultMinCache
	}
	ttl := cacheLifetime(rec.TTL)
	if rec.Cached < minCache || now.Sub(time.UnixMilli(rec.Last)) >= ttl-warmMargin {
		return false // cold: switching costs nothing more than staying
	}
	pw, calls := float64(rec.Cached), float64(rec.Calls)
	// A turn's cost on a model while warm: its calls reading the cached
	// prefix, writing what each adds, and its output.
	turn := func(s switchSide, scale float64) float64 {
		read, write := cachePrices(s.model, s.kind, rec.TTL, s.misses)
		return read*calls*pw + write*float64(rec.CacheWrite) + s.model.Output*float64(rec.Output)*scale
	}
	// The next model writes what it writes against the current one.
	scale := 1.0
	if cur.outputScale > 0 && next.outputScale > 0 {
		scale = cur.outputScale / next.outputScale
	}
	saving := turn(cur, 1) - turn(next, scale)
	readNext, writeNext := cachePrices(next.model, next.kind, rec.TTL, next.misses)
	rebuild := (writeNext - readNext) * pw
	ahead := float64(min(max(humanTurns-1, 1), maxTurnsAhead))
	return ahead*saving < rebuild
}

// cacheLifetime is how long a cache entry lives after its last use: an
// hour for one-hour entries, else five minutes (Anthropic's default, and
// about what implicit caches keep).
func cacheLifetime(ttl string) time.Duration {
	if ttl == "1h" {
		return time.Hour
	}
	return 5 * time.Minute
}

// cachePrices are a model's prices for reading and writing cached input,
// per million tokens: providers that cache implicitly (OpenAI's kind)
// charge no write premium, a write costs the input price there. A read
// the cache misses (misses: its share) costs the input price.
func cachePrices(m llm.Model, kind, ttl string, misses float64) (read, write float64) {
	read = m.CachedInput
	if read <= 0 {
		read = m.Input
	}
	read += min(max(misses, 0), 1) * (m.Input - read)
	write = m.CacheWrite
	if ttl == "1h" {
		write = m.CacheWrite1h
		if write <= 0 {
			write = 2 * m.Input
		}
	}
	if write <= 0 || kind != "anthropic" && m.CacheWrite <= 0 {
		write = m.Input
	}
	return read, write
}
