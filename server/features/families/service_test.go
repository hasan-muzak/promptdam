package families_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hasan-muzak/keel/pkg/events"

	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/families"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/gatewaytest"
)

func TestFamiliesFromCalls(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	ctx := context.Background()
	chat := func(system string, header ...string) {
		t.Helper()
		body := fmt.Sprintf(`{"model":"gpt-5-mini","messages":[{"role":"system","content":%q},{"role":"user","content":"hi"}]}`, system)
		if status, _, b := w.Call("/v1/chat/completions", body, header...); status != 200 {
			t.Fatalf("%d %s", status, b)
		}
	}
	// One template filled with three dates: one family, three prefixes.
	for _, day := range []string{"2026-10-05", "2026-10-06", "2026-10-07"} {
		chat("Summarise the tickets of " + day + ".")
	}
	chat("Translate to French.")
	chat("Anything at all.", brand.HeaderFamily, "invoice-extract")
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	s := families.NewService(w.App.Ent, w.C.Events)
	now := time.Now().UTC()
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	top, err := s.Top(w.Scoped(), w.Project, from, to, 10)
	if err != nil || len(top) != 3 || top[0].Requests != 3 {
		t.Fatalf("top %+v, %v", top, err)
	}
	summaries := top[0].ID
	if err := s.SetLabel(w.Scoped(), w.Org, w.Project, summaries, "Ticket summaries"); err != nil {
		t.Fatal(err)
	}
	d, err := s.Get(w.Scoped(), w.Project, summaries, from, to)
	if err != nil || d.Label != "Ticket summaries" || len(d.Models) != 1 || d.Models[0].ServedModel != "gpt-5-mini" ||
		len(d.Series) != 1 || d.CostMicros <= 0 {
		t.Fatalf("detail %+v, %v", d, err)
	}
	if _, err := s.Get(w.Scoped(), w.Project, "name:invoice-extract", from, to); err != nil {
		t.Fatalf("a named family: %v", err)
	}
	if _, err := s.Get(w.Scoped(), w.Project, "../x", from, to); !errors.Is(err, families.ErrUnknown) {
		t.Fatalf("a bad ID: %v", err)
	}

	// The cache doctor's rollup tells the three prefixes of one family apart,
	// and knows they hold dates.
	var prefixes []struct {
		Prefixes uint64 `ch:"prefixes"`
		Flags    uint16 `ch:"flags"`
	}
	if err := w.C.Events.Select(ctx, &prefixes, events.Project(w.Project),
		`SELECT uniqExact(prefix_hash) AS prefixes, groupBitOr(prefix_flags) AS flags FROM prefixes_hourly
		 WHERE {tenant} AND family_id = ?`, summaries); err != nil {
		t.Fatal(err)
	}
	if prefixes[0].Prefixes != 3 || families.Flags(prefixes[0].Flags)&families.Dates == 0 {
		t.Fatalf("prefixes %+v", prefixes)
	}
}
