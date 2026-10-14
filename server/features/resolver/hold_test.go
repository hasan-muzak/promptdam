package resolver

import (
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/features/conversation"
)

func TestHoldsWarm(t *testing.T) {
	now := time.Now()
	opus := switchSide{model: llm.Model{Input: 4, CachedInput: 0.4, CacheWrite: 5, Output: 20, MinCacheTokens: 512}, kind: "anthropic", outputScale: 1}
	sonnet := switchSide{model: llm.Model{Input: 2, CachedInput: 0.2, CacheWrite: 2.5, Output: 10, MinCacheTokens: 512}, kind: "anthropic", outputScale: 1}
	nearly := switchSide{model: llm.Model{Input: 3.8, CachedInput: 0.38, CacheWrite: 4.75, Output: 19, MinCacheTokens: 512}, kind: "anthropic", outputScale: 1}
	thinksLess := opus
	thinksLess.outputScale = 3 // the default writes 3x what this tier does
	gpt := switchSide{model: llm.Model{Input: 2, CachedInput: 0.2, Output: 8}, kind: "openai", outputScale: 1}
	gptMini := switchSide{model: llm.Model{Input: 1.9, CachedInput: 0.19, Output: 7.6}, kind: "openai", outputScale: 1}
	warm := conversation.Record{Calls: 8, CacheWrite: 30_000, Output: 20_000, Cached: 100_000, Last: now.Add(-time.Minute).UnixMilli(), TTL: "5m"}

	tests := []struct {
		name      string
		cur, next switchSide
		rec       func(conversation.Record) conversation.Record
		humans    int
		hold      bool
	}{
		{"a much cheaper model pays for the rebuild at once", opus, sonnet, nil, 2, false},
		{"a slightly cheaper one doesn't", opus, nearly, nil, 2, true},
		{"over four turns it does", opus, nearly, func(r conversation.Record) conversation.Record { r.Cached = 20_000; return r }, 9, false},
		{"the same model at an unmeasured lower effort: nothing measured to save", opus, opus, nil, 3, true},
		{"the same model measured writing a third: switch", opus, thinksLess, nil, 3, false},
		{"a cold cache: switching costs nothing more", opus, nearly, func(r conversation.Record) conversation.Record {
			r.Last = now.Add(-6 * time.Minute).UnixMilli()
			return r
		}, 2, false},
		{"a one-hour cache still warm", opus, nearly, func(r conversation.Record) conversation.Record {
			r.Last, r.TTL = now.Add(-20*time.Minute).UnixMilli(), "1h"
			return r
		}, 2, true},
		{"a prefix too short to cache", opus, nearly, func(r conversation.Record) conversation.Record { r.Cached = 300; return r }, 2, false},
		{"an unpriced model never holds", opus, switchSide{kind: "anthropic"}, nil, 2, false},
		{"implicit caches: no write premium, the rebuild is the read discount", gpt, gptMini, nil, 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := warm
			if tt.rec != nil {
				rec = tt.rec(rec)
			}
			if got := holdsWarm(tt.cur, tt.next, rec, tt.humans, now); got != tt.hold {
				t.Fatalf("holds %v, want %v", got, tt.hold)
			}
		})
	}
}
