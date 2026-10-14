package cacheprofile

import (
	"slices"
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/promptcache"
)

// row is calls with a strategy to a backend: expects of them expected a
// read and hits read, each expecting 1000 tokens (all read on a hit); the
// input cost price micro-dollars per 1000 tokens.
func row(kind, model, backend, strategy string, expects, hits int64, price float64) Row {
	return Row{Kind: kind, Model: model, Backend: backend, Strategy: strategy, Expects: expects, Hits: hits,
		Expected: expects * 1000, Read: hits * 1000, Input: (expects + 10) * 1000, Priced: int64(price * float64(expects+10))}
}

func TestLearn(t *testing.T) {
	const or, gemini = "openrouter", "google/gemini-3.8-flash"
	markers, auto := promptcache.Markers, promptcache.Auto
	def := func(kind, model string) string { return gateway.DefaultCacheStrategy(kind, false, model == gemini) }
	prevUsing := func(use string) *Doc {
		return &Doc{Models: map[string]Profile{Key(or, gemini): {Use: use}}}
	}
	tests := []struct {
		name  string
		rows  []Row
		prev  *Doc
		use   string
		trial string
		avoid []string
	}{
		{"a new model: its price list's, the other tried", []Row{row(or, gemini, "Google AI Studio", markers, 40, 38, 300)},
			nil, markers, auto, nil},
		{"never seen, the other caches well: switched", []Row{row(or, gemini, "Google AI Studio", auto, 400, 380, 300)},
			prevUsing(markers), auto, "", nil},
		{"cheaper by more than a tenth: switched", []Row{
			row(or, gemini, "Google AI Studio", auto, 400, 0, 1000), row(or, gemini, "Google AI Studio", markers, 400, 380, 850)},
			prevUsing(auto), markers, "", nil},
		{"unpriced: never judged, still tried", []Row{
			row(or, gemini, "Google AI Studio", auto, 400, 0, 0), row(or, gemini, "Google AI Studio", markers, 400, 380, 0)},
			prevUsing(auto), auto, markers, nil},
		{"cheaper by less: kept, and no longer tried", []Row{
			row(or, gemini, "Google AI Studio", auto, 400, 0, 1000), row(or, gemini, "Google AI Studio", markers, 400, 380, 950)},
			prevUsing(auto), auto, "", nil},
		{"healthy, the other untried: not tried", []Row{row(or, gemini, "Google AI Studio", markers, 400, 380, 300)},
			prevUsing(markers), markers, "", nil},
		{"unhealthy, the other untried: tried", []Row{row(or, gemini, "Google AI Studio", markers, 400, 40, 900)},
			prevUsing(markers), markers, auto, nil},
		{"a backend that never reads while another does: avoided", []Row{
			row(or, gemini, "Google AI Studio", markers, 400, 390, 300), row(or, gemini, "Google", markers, 150, 2, 1100)},
			prevUsing(markers), markers, "", []string{"Google"}},
		{"too few calls to judge the backend", []Row{
			row(or, gemini, "Google AI Studio", markers, 400, 390, 300), row(or, gemini, "Google", markers, 60, 0, 1100)},
			prevUsing(markers), markers, "", nil},
		{"no backend reads: none avoided", []Row{
			row(or, gemini, "Google AI Studio", auto, 400, 2, 1000), row(or, gemini, "Google", auto, 400, 0, 1000)},
			prevUsing(auto), auto, markers, nil},
	}
	for _, tt := range tests {
		got := learn(tt.rows, map[string]int{Key(or, gemini): 4}, tt.prev, def)[Key(or, gemini)]
		if got.Use != tt.use || got.Trial != tt.trial || !slices.Equal(got.avoid, tt.avoid) || got.Projects != 4 {
			t.Errorf("%s: %+v (avoid %v)", tt.name, got.Profile, got.avoid)
		}
	}

	// A kind with one strategy is never tried against, nor kept off
	// backends; its read share is still learned.
	got := learn([]Row{row("anthropic", "claude-opus-5-5", "", markers, 400, 300, 200),
		row("custom", "llama", "a", markers, 400, 390, 300), row("custom", "llama", "b", markers, 150, 0, 900)}, nil, nil, def)
	if a := got[Key("anthropic", "claude-opus-5-5")]; a.Use != markers || a.Trial != "" || a.ReadShare != 0.75 || a.Evidence != 400 {
		t.Errorf("anthropic: %+v", a.Profile)
	}
	if c := got[Key("custom", "llama")]; c.avoid != nil {
		t.Errorf("custom endpoints' backends: %v", c.avoid)
	}
}

// A model is kept on one backend per conversation only when several serve
// it: backends avoided, or answering a sliver of its calls, don't count.
func TestServing(t *testing.T) {
	const or, ds = "openrouter", "deepseek/deepseek-v4.1-flash"
	call := func(backend string, calls int64) Row {
		return Row{Kind: or, Model: ds, Backend: backend, Strategy: promptcache.Auto, Calls: calls}
	}
	def := func(string, string) string { return promptcache.Auto }
	for _, tt := range []struct {
		name string
		rows []Row
		want int
	}{
		{"one backend", []Row{call("OpenAI", 500)}, 1},
		{"several", []Row{call("CoreWeave", 300), call("Modal", 200), call("Relace", 40)}, 3},
		{"a sliver doesn't count", []Row{call("CoreWeave", 500), call("Modal", 2)}, 1},
	} {
		if got := learn(tt.rows, nil, nil, def)[Key(or, ds)].Backends; got != tt.want {
			t.Errorf("%s: %d backends, want %d", tt.name, got, tt.want)
		}
	}
	// Avoided backends serve no conversation.
	gemini := []Row{row(or, "g", "Google AI Studio", promptcache.Markers, 400, 390, 300), row(or, "g", "Google", promptcache.Markers, 150, 0, 900)}
	gemini[0].Calls, gemini[1].Calls = 450, 160
	if l := learn(gemini, nil, nil, func(string, string) string { return promptcache.Markers })[Key(or, "g")]; l.Backends != 1 || len(l.avoid) != 1 {
		t.Errorf("gemini: %d backends, avoid %v", l.Backends, l.avoid)
	}
}
