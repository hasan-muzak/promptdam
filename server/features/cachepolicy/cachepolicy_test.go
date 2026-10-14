package cachepolicy_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/cachedoctor"
	"github.com/hasan-muzak/promptdam/server/features/cachettl"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

// A family whose conversations come back 5 to 60 minutes later is cached
// an hour: the learner weighs its calls' gaps, gateways mark its calls
// with the longer lifetime, and the cache doctor says so, with the calls
// that wrote its prefix at the same moment.
func TestFamiliesCachedAnHour(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	ctx := context.Background()
	w.Grant(catalog.PromptCaching)
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-sonnet-5-5"})
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	system, _ := json.Marshal(strings.Repeat("You review contracts for the legal team. ", 80))
	call := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":` + string(system) + `,"messages":[{"role":"user","content":"a"},
		{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	req, err := wire.ParseRequest(wire.AnthropicMessages, []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	shape := families.Of(req.Prefix, "")

	// Three days of the family's calls: 40 came back 20 minutes after
	// their conversation's previous one, 40 within a minute, 10 started a
	// conversation, and 5 wrote the prefix within a second of each other.
	batch, err := w.C.Events.Unscoped().PrepareBatch(ctx, `INSERT INTO requests (project_id, ts, trace, org_id, family_id,
		prefix_hash, prefix_tokens, served_model, kind, usage_known, input_tokens, cache_read_tokens, cache_write_tokens,
		chain_state, chain_gap_ms, retention_days)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	add := func(i int, at time.Time, state string, gap uint32, read, write uint32) {
		if err := batch.Append(w.Project, at, "req_"+string(rune('a'+i%26))+at.Format("150405.000"), w.Org, shape.Family, shape.Prefix,
			uint32(800), "claude-sonnet-5-5", "anthropic", true, read+write+50, read, write, state, gap, uint16(7)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 40 {
		add(i, now.Add(-time.Duration(i)*25*time.Minute), "continued", 20*60_000, 0, 50_000)
		add(i, now.Add(-time.Duration(i)*25*time.Minute-21*time.Minute), "continued", 60_000, 48_000, 2_000)
	}
	for i := range 10 {
		add(i, now.Add(-time.Duration(i)*7*time.Hour-3*time.Minute), "start", 0, 0, 50_000)
	}
	burst := now.Add(-30 * time.Hour)
	for i := range 5 {
		add(i, burst.Add(time.Duration(i)*200*time.Millisecond), "start", 0, 0, 50_000)
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}

	if err := w.App.CachePolicy.Learn(w.Scoped(), w.Org, w.Project); err != nil {
		t.Fatal(err)
	}
	fams, err := w.App.CachePolicy.Families(w.Scoped(), w.Project)
	if err != nil {
		t.Fatal(err)
	}
	f := fams[shape.Family]
	if f.Tail.TTL != cachettl.OneHour || f.TTLOf().Prefix != cachettl.OneHour || f.Model != "claude-sonnet-5-5" ||
		f.ColdParallel < 4 || f.ColdParallelMicros <= 0 {
		t.Fatalf("learned %+v", f)
	}

	// The gateway keeps the family's prefix an hour.
	w.Eventually(func() bool {
		if status, _, _ := w.Call("/v1/messages", call); status != 200 {
			return false
		}
		reqs := w.Fake.Requests()
		var sent struct {
			System []struct {
				CacheControl struct {
					TTL string `json:"ttl"`
				} `json:"cache_control"`
			} `json:"system"`
		}
		return json.Unmarshal(reqs[len(reqs)-1].Body, &sent) == nil && len(sent.System) == 1 && sent.System[0].CacheControl.TTL == "1h"
	})

	// The doctor says so.
	doctor := cachedoctor.New(w.C.Events, w.App.Prices, w.App.CachePolicy)
	ds, err := doctor.Diagnose(w.Scoped(), w.Project, shape.Family, time.Now().Add(-4*24*time.Hour), time.Now().Add(time.Hour))
	if err != nil || len(ds) != 1 {
		t.Fatalf("%+v %v", ds, err)
	}
	codes := map[string]bool{}
	for _, fd := range ds[0].Findings {
		codes[fd.Code] = true
	}
	if !codes[cachedoctor.KeptOneHour] || !codes[cachedoctor.ColdParallel] || ds[0].TTL.Tail != cachettl.OneHour {
		t.Fatalf("diagnosis %+v", ds[0])
	}
}
