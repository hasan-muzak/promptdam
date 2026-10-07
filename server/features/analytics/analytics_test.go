package analytics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
)

func TestRequestLogAndSpend(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	ctx := context.Background()
	var traces []string
	for i, model := range []string{"gpt-5-mini", "gpt-5-mini", "gpt-5-nano", "gpt-5"} {
		tag := "a"
		if i%2 == 1 {
			tag = "b"
		}
		status, h, body := w.Call("/v1/chat/completions", `{"model":"`+model+`","messages":[]}`, brand.HeaderTags, tag)
		if status != 200 {
			t.Fatalf("%d %s", status, body)
		}
		traces = append(traces, h.Get(brand.HeaderTrace))
		time.Sleep(2 * time.Millisecond) // distinct timestamps
	}
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	s := analytics.New(w.C.Events)
	now := time.Now().UTC()
	from, to := now.Add(-time.Hour), now.Add(time.Minute)

	// Newest first, in pages.
	page, next, err := s.Requests(ctx, w.Project, analytics.Filter{From: from, To: to}, nil, 3)
	if err != nil || len(page) != 3 || next == nil || page[0].Trace != traces[3] {
		t.Fatalf("first page %d rows, next %v, err %v", len(page), next, err)
	}
	cursor, err := analytics.DecodeCursor(next.Encode())
	if err != nil {
		t.Fatal(err)
	}
	rest, next, err := s.Requests(ctx, w.Project, analytics.Filter{From: from, To: to}, &cursor, 3)
	if err != nil || len(rest) != 1 || next != nil || rest[0].Trace != traces[0] {
		t.Fatalf("last page %+v, next %v, err %v", rest, next, err)
	}
	only, _, err := s.Requests(ctx, w.Project, analytics.Filter{From: from, To: to, Model: "gpt-5-nano"}, nil, 10)
	if err != nil || len(only) != 1 || only[0].KeyID != w.Key {
		t.Fatalf("filtered %+v, %v", only, err)
	}
	one, err := s.Request(ctx, w.Project, traces[1])
	if err != nil || one.ServedModel != "gpt-5-mini" || one.Tags[0] != "b" || one.CostMicros <= 0 {
		t.Fatalf("by trace %+v, %v", one, err)
	}
	if _, err := s.Request(ctx, uuid.New(), traces[1]); !errors.Is(err, analytics.ErrNotFound) {
		t.Fatalf("another project's trace: %v", err)
	}

	spend, err := s.Spend(ctx, w.Project, from, to, "model", "hour")
	if err != nil || spend.Total.Requests != 4 || len(spend.Groups) != 3 || len(spend.Series) != 1 {
		t.Fatalf("spend %+v, %v", spend, err)
	}
	byTag, err := s.Spend(ctx, w.Project, from, to, "tag", "day")
	if err != nil || len(byTag.Groups) != 2 || byTag.Groups[0].Requests != 2 {
		t.Fatalf("by tag %+v, %v", byTag, err)
	}
	lat, err := s.Latency(ctx, w.Project, from, to, "provider")
	if err != nil || len(lat) != 1 || lat[0].Requests != 4 || len(lat[0].Total) != 3 {
		t.Fatalf("latency %+v, %v", lat, err)
	}
	if _, err := s.Spend(ctx, w.Project, to, from, "", "day"); !errors.Is(err, analytics.ErrRange) {
		t.Fatalf("a reversed range: %v", err)
	}
}

// Lookups by trace read a moment of one project's rows, pages read the
// newest granules, and spend reads the rollups, however many calls the log
// holds (200,000 here, two projects interleaved over five hours).
func TestScanBudgets(t *testing.T) {
	ctx := context.Background()
	db := testkit.ClickHouse(t)
	if err := db.Migrate(ctx, migrationsSet()); err != nil {
		t.Fatal(err)
	}
	project, other := uuid.New(), uuid.New()
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-5 * time.Hour)
	err := db.Unscoped().Exec(ctx, `INSERT INTO requests (project_id, ts, trace, served_model, cost_micros, retention_days)
		SELECT if(number % 2 = 0, toUUID(?), toUUID(?)), fromUnixTimestamp64Milli(toInt64(? + number * 90)),
		       concat('req_', toString(number)), 'gpt-5-mini', 10, 7
		FROM numbers(200000)`, project.String(), other.String(), start.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	// One call with a real trace, at the time its trace holds.
	id := ids.New()
	trace := ids.Encode(analytics.TracePrefix, id)
	sec, nsec := id.Time().UnixTime()
	err = db.Unscoped().Exec(ctx, `INSERT INTO requests (project_id, ts, trace, served_model, retention_days)
		VALUES (?, fromUnixTimestamp64Milli(?), ?, 'gpt-5', 7)`, project, time.Unix(sec, nsec).UnixMilli(), trace)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Exec(ctx, `OPTIMIZE TABLE requests FINAL`); err != nil {
		t.Fatal(err)
	}
	s := analytics.New(db)
	if r, err := s.Request(ctx, project, trace); err != nil || r.ServedModel != "gpt-5" {
		t.Fatalf("by trace %+v, %v", r, err)
	}
	for name, tt := range map[string]struct {
		budget uint64
		fn     func(ctx context.Context)
	}{
		"trace": {2 * 8192, func(ctx context.Context) { _, _ = s.Request(ctx, project, trace) }},
		"page": {3 * 8192, func(ctx context.Context) {
			_, _, _ = s.Requests(ctx, project, analytics.Filter{From: start, To: end.Add(time.Minute)}, nil, 50)
		}},
		"spend": {2048, func(ctx context.Context) {
			_, _ = s.Spend(ctx, project, start, end.Add(time.Hour), "model", "hour")
		}},
	} {
		read := testkit.ReadRows(t, db, tt.fn)
		t.Logf("%-6s read %7d rows (budget %d)", name, read, tt.budget)
		if read == 0 || read > tt.budget {
			t.Errorf("%s read %d rows, budget %d", name, read, tt.budget)
		}
	}
}
