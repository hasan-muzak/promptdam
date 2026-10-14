package requestlog

import (
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/conversation"
)

// A call should read from the cache what its conversation's previous call
// sent, when nothing between them should have lost it.
func TestExpected(t *testing.T) {
	prev := conversation.Previous{Target: "google/gemini-3.8-flash", Kind: "openrouter", Backend: "Google AI Studio", Strategy: "markers",
		TTL: "5m", Input: 4000}
	cur := conversation.Finished{Target: "google/gemini-3.8-flash", Kind: "openrouter", Backend: "Google AI Studio", Strategy: "markers",
		Input: 4500}
	tests := []struct {
		name  string
		state string
		gap   int64
		p     conversation.Previous
		c     conversation.Finished
		want  int64
	}{
		{"a minute later", conversation.ChainContinued, 60_000, prev, cur, 4000},
		{"the conversation's first call", conversation.ChainStart, 0, prev, cur, 0},
		{"the store unread", conversation.ChainUnknown, 60_000, prev, cur, 0},
		{"past five minutes", conversation.ChainContinued, 290_000, prev, cur, 0},
		{"within an hour's entry", conversation.ChainContinued, 1_800_000,
			func() conversation.Previous { p := prev; p.TTL = "1h"; return p }(), cur, 4000},
		{"another model", conversation.ChainContinued, 60_000, prev, func() conversation.Finished { c := cur; c.Target = "x"; return c }(), 0},
		{"another backend", conversation.ChainContinued, 60_000, prev, func() conversation.Finished { c := cur; c.Backend = "Google"; return c }(), 0},
		{"another strategy", conversation.ChainContinued, 60_000, prev, func() conversation.Finished { c := cur; c.Strategy = "auto"; return c }(), 0},
		{"a route that doesn't cache", conversation.ChainContinued, 60_000, prev, func() conversation.Finished { c := cur; c.Strategy = ""; return c }(), 0},
		{"too short to cache", conversation.ChainContinued, 60_000, func() conversation.Previous { p := prev; p.Input = 900; return p }(), cur, 0},
		{"a shorter call (compacted)", conversation.ChainContinued, 60_000, prev, func() conversation.Finished { c := cur; c.Input = 3000; return c }(), 0},
	}
	for _, tt := range tests {
		if got := expected(tt.state, tt.gap, tt.p, tt.c, 1024); got != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, got, tt.want)
		}
	}
}
