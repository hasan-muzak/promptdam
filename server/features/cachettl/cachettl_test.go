package cachettl

import (
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm"
)

func TestDecide(t *testing.T) {
	// Sonnet-like prices: read 0.3, 5-minute write 3.75, one-hour write 6.
	m := llm.Model{Input: 3, CachedInput: 0.3, CacheWrite: 3.75, CacheWrite1h: 6, Output: 15}
	tests := []struct {
		name string
		g    Gaps
		prev string
		ttl  string
	}{
		// An hour gains (3.75-0.3)×mid and costs (6-3.75)×(inc+long):
		// 3.45×100k = 345k against 2.25×100k = 225k, a ratio of 1.53.
		{"returns 5 to 60 minutes later pay for the hour", Gaps{Mid: 100_000, Inc: 60_000, Long: 40_000, MidCalls: 30}, "", OneHour},
		{"too few returns to tell", Gaps{Mid: 100_000, Inc: 60_000, Long: 40_000, MidCalls: 5}, "", ""},
		{"writes within five minutes outweigh them", Gaps{Mid: 100_000, Inc: 300_000, Long: 40_000, MidCalls: 30}, "", ""},
		// A ratio of 1.0: not enough to start, enough to keep.
		{"a narrow gain doesn't start an hour", Gaps{Mid: 65_217, Inc: 60_000, Long: 40_000, MidCalls: 30}, "", ""},
		{"nor stops one", Gaps{Mid: 65_217, Inc: 60_000, Long: 40_000, MidCalls: 30}, OneHour, OneHour},
		{"a clear loss stops it", Gaps{Mid: 20_000, Inc: 60_000, Long: 40_000, MidCalls: 30}, OneHour, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if d := Decide(tt.g, m, tt.prev); d.TTL != tt.ttl {
				t.Fatalf("Decide = %+v, want %q", d, tt.ttl)
			}
		})
	}
	if d := Decide(Gaps{Mid: 1e6, MidCalls: 100}, llm.Model{Input: 3}, ""); d.TTL != "" {
		t.Fatalf("unknown cache prices decided %+v", d)
	}
	// Without a listed one-hour price, it is twice the input.
	if d := Decide(Gaps{Mid: 100_000, Inc: 60_000, Long: 40_000, MidCalls: 30}, llm.Model{Input: 3, CachedInput: 0.3, CacheWrite: 3.75}, ""); d.TTL != OneHour {
		t.Fatalf("a default one-hour price %+v", d)
	}
	// A one-hour tail keeps the prefix an hour too.
	if got := (Family{Tail: Decision{TTL: OneHour}}).TTLOf(); got != (TTL{Prefix: OneHour, Tail: OneHour}) {
		t.Fatalf("TTLOf %+v", got)
	}
}
