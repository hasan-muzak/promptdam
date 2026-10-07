package cachedoctor_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/llm"
	"github.com/hasan-muzak/keel/pkg/testkit"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/cachedoctor"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/families"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/prices"
	"github.com/hasan-muzak/keel/products/promptdam/server/migrations"
)

// Four families of 100 calls each, written to the request log:
//
//	dated    a date in an Anthropic prompt of ~3,000 tokens: 100 prefixes
//	marked   a stable Anthropic prompt the client never marked
//	healthy  a stable OpenAI prompt the cache served
//	short    a 200-token prompt: nothing to cache
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
		err := db.Unscoped().Exec(ctx, `INSERT INTO requests (project_id, ts, trace, family_id, prefix_hash, prefix_flags,
			prefix_tokens, kind, served_model, usage_known, input_tokens, cache_read_tokens, retention_days)
			SELECT ?, now64(3) - toIntervalSecond(number), toString(number), ?, `+prefixExpr+`, ?, ?, ?, ?, true, ?, ?, 7
			FROM numbers(100)`, project, family, prefix, uint16(flags), tokens, kind, model, tokens+50, cacheRead)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("dated", "p", true, families.Dates|families.Breakpoints, 3000, 0, "anthropic", "claude-sonnet-5-5")
	insert("marked", "m", false, 0, 3000, 0, "anthropic", "claude-sonnet-5-5")
	insert("healthy", "h", false, 0, 3000, 2900, "openai", "gpt-5-mini")
	insert("short", "s", true, families.Numbers, 200, 0, "openai", "gpt-5-mini")

	s := cachedoctor.New(db, prices.Static{Catalog: llm.Default()})
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
	if len(got) != 4 {
		t.Fatalf("%d diagnoses", len(got))
	}
	want := map[string]string{"dated": cachedoctor.Timestamps, "marked": cachedoctor.NoBreakpoints,
		"healthy": cachedoctor.OK, "short": cachedoctor.TooShort}
	for family, code := range want {
		if d := got[family]; d.Findings[0].Code != code {
			t.Errorf("%s: %+v", family, d.Findings)
		}
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

	one, err := s.Diagnose(ctx, project, "marked", now.Add(-24*time.Hour), now.Add(time.Hour))
	if err != nil || len(one) != 1 || one[0].Prefixes != 1 || one[0].PrefixTokens != 3000 {
		t.Fatalf("one family %+v, %v", one, err)
	}
}
