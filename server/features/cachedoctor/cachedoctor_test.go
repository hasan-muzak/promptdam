package cachedoctor_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/server/features/cachedoctor"
	"github.com/hasan-muzak/promptdam/server/features/cacheprofile"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/promptcache"
	"github.com/hasan-muzak/promptdam/server/migrations"
)

// Families of 100 calls each, written to the request log:
//
//	dated     a date in an Anthropic prompt of ~3,000 tokens: 100 prefixes
//	marked    a stable Anthropic prompt the client never marked
//	healthy   a stable OpenAI prompt the cache served
//	short     a 200-token prompt: nothing to cache
//	gemini    a stable prompt to Gemini on OpenRouter, which caches only
//	          marked prompts, unmarked
//	avoided   the same, marked by the gateway and cached; every project's
//	          calls showed a backend that never caches it
//	never     a stable prompt to a model that, across projects, never
//	          reads its cache
//	few       the same, learned from too few projects to be shown
func TestDiagnose(t *testing.T) {
	ctx := context.Background()
	db := testkit.ClickHouse(t, migrations.ClickHouse())
	project := uuid.New()
	insert := func(family, prefix string, distinct bool, flags families.Flags, tokens, cacheRead uint32, kind, model string) {
		t.Helper()
		prefixExpr := "?"
		if distinct {
			prefixExpr = "concat(?, toString(number))"
		}
		// A router of providers names the model it served its own way.
		served := model
		if kind == "openrouter" {
			served += "-20261001"
		}
		err := db.Unscoped().Exec(ctx, `INSERT INTO requests (project_id, ts, trace, family_id, prefix_hash, prefix_flags,
			prefix_tokens, kind, served_model, target_model, usage_known, input_tokens, cache_read_tokens, retention_days)
			SELECT ?, now64(3) - toIntervalSecond(number), toString(number), ?, `+prefixExpr+`, ?, ?, ?, ?, ?, true, ?, ?, 7
			FROM numbers(100)`, project, family, prefix, uint16(flags), tokens, kind, served, model, tokens+50, cacheRead)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("dated", "p", true, families.Dates|families.Breakpoints, 3000, 0, "anthropic", "claude-sonnet-5-5")
	insert("marked", "m", false, 0, 3000, 0, "anthropic", "claude-sonnet-5-5")
	insert("healthy", "h", false, 0, 3000, 2900, "openai", "gpt-5-mini")
	insert("short", "s", true, families.Numbers, 200, 0, "openai", "gpt-5-mini")
	insert("gemini", "g", false, 0, 3000, 0, "openrouter", "google/gemini-3.8-flash")
	insert("avoided", "a", false, families.GatewayBreakpoints, 3000, 2900, "openrouter", "google/gemini-3.8-flash")
	insert("never", "n", false, 0, 3000, 0, "openrouter", "acme/never-caches")
	insert("few", "f", false, 0, 3000, 0, "openrouter", "acme/rarely-used")

	profiles := profiles{Models: map[string]cacheprofile.Profile{
		cacheprofile.Key("openrouter", "google/gemini-3.8-flash"): {Use: promptcache.Markers, Avoid: []string{"google-vertex"},
			ReadShare: 0.9, Evidence: 5000, Projects: 12},
		cacheprofile.Key("openrouter", "acme/never-caches"): {Use: promptcache.Auto, ReadShare: 0.01, Evidence: 900, Projects: 4},
		cacheprofile.Key("openrouter", "acme/rarely-used"):  {Use: promptcache.Auto, ReadShare: 0, Evidence: 900, Projects: 2},
	}}
	s := cachedoctor.New(db, prices.Static{Catalog: llm.Default()}, nil, profiles)
	now := time.Now().UTC()
	ds, err := s.Diagnose(ctx, project, "", now.Add(-24*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]cachedoctor.Diagnosis{}
	for _, d := range ds {
		got[d.Family] = d
		t.Logf("%-8s %-22s prefixes %3d hit %.2f potential $%.4f/month", d.Family, d.Findings[0].Code, d.Prefixes,
			d.HitRate, float64(d.PotentialMonthlyMicros)/1e6)
	}
	if len(got) != 8 {
		t.Fatalf("%d diagnoses", len(got))
	}
	want := map[string][]string{"dated": {cachedoctor.Timestamps}, "marked": {cachedoctor.NoBreakpoints},
		"healthy": {cachedoctor.OK}, "short": {cachedoctor.TooShort}, "gemini": {cachedoctor.NoBreakpoints, cachedoctor.BackendNeverCaches},
		"avoided": {cachedoctor.OK, cachedoctor.BackendNeverCaches}, "never": {cachedoctor.ModelNeverCaches}, "few": {cachedoctor.OK}}
	for family, codes := range want {
		d := got[family]
		var have []string
		for _, f := range d.Findings {
			have = append(have, f.Code)
		}
		if !slices.Equal(have, codes) {
			t.Errorf("%s: %v, want %v", family, have, codes)
		}
	}
	if d := got["gemini"]; d.Target != "google/gemini-3.8-flash" || d.Model != "google/gemini-3.8-flash-20261001" {
		t.Errorf("gemini's models: %q, %q", d.Target, d.Model)
	}
	// 99 calls × 3,000 tokens at Sonnet's $3, cached at $0.30, over a day:
	// $0.80, about $24 a month.
	if p := got["dated"].PotentialMonthlyMicros; p < 23_000_000 || p > 25_000_000 {
		t.Fatalf("dated potential %d", p)
	}
	if got["healthy"].PotentialMonthlyMicros != 0 || got["short"].PotentialMonthlyMicros != 0 {
		t.Fatal("nothing to fix, yet a potential")
	}
	// The worst first.
	if ds[0].PotentialMonthlyMicros < ds[len(ds)-1].PotentialMonthlyMicros {
		t.Fatal("not ordered by potential")
	}

	if f := got["few"].Findings[0]; f.Title != "The prefix is stable" {
		t.Errorf("a stable prefix the cache never served: %+v", f)
	}

	one, err := s.Diagnose(ctx, project, "marked", now.Add(-24*time.Hour), now.Add(time.Hour))
	if err != nil || len(one) != 1 || one[0].Prefixes != 1 || one[0].PrefixTokens != 3000 {
		t.Fatalf("one family %+v, %v", one, err)
	}
}

// profiles are learned profiles, fixed.
type profiles cacheprofile.Doc

func (p profiles) Current() *cacheprofile.Doc {
	d := cacheprofile.Doc(p)
	return &d
}
