package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/ent/webhookdelivery"
	"github.com/hasan-muzak/keel-core/pkg/entx"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/activity"
	"github.com/hasan-muzak/promptdam/server/features/alerts"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
)

// calls writes n calls of the last minutes to the request log, failing
// of them failing at the provider.
func calls(t *testing.T, w *gatewaytest.World, at time.Time, n, failing int, provider string, latencyMs uint32, costMicros int64) {
	t.Helper()
	err := w.C.Events.Unscoped().Exec(context.Background(), `INSERT INTO requests (project_id, ts, trace, org_id, provider,
		served_model, status, error_code, latency_ms, cost_micros, usage_known, retention_days)
		SELECT ?, fromUnixTimestamp64Milli(?) - toIntervalSecond(number % 60), concat(?, toString(number)), ?, ?, 'gpt-5-mini',
		if(number < ?, 502, 200), if(number < ?, 'provider_unreachable', ''), ?, ?, true, 7 FROM numbers(?)`,
		w.Project, at.UnixMilli(), provider+at.String(), w.Org, provider, failing, failing, latencyMs, costMicros, n)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEvaluate(t *testing.T) {
	ctx := context.Background()
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	w.Grant(catalog.Alerts)
	all := entx.SkipTenant(ctx)
	w.C.DB.Ent.WebhookEndpoint.Create().SetOrgID(w.Org).SetURL("https://hooks.example.com/x").SetSecret("whsec_dGVzdA==").SaveX(all)
	s := w.App.Alerts
	rules := map[string]alerts.Input{
		"errors":     {Kind: alerts.ErrorRate},
		"down":       {Kind: alerts.ProviderDown},
		"slow":       {Kind: alerts.Latency, Threshold: 1000},
		"spike":      {Kind: alerts.SpendSpike},
		"quiet":      {Kind: alerts.ErrorRate, MinCalls: 1000},
		"backup":     {Kind: alerts.ErrorRate, Provider: "backup", Threshold: 0.9},
		"turned off": {Kind: alerts.ErrorRate},
	}
	ids := map[string]uuid.UUID{}
	for name, in := range rules {
		in.Name, in.Enabled = name, name != "turned off"
		r, err := s.Create(w.Scoped(), w.Org, w.Project, in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ids[name] = r.ID
	}

	now := time.Now()
	// A usual week: $0.10 an hour at main.
	for h := 2; h < 7*24; h += 24 {
		calls(t, w, now.Add(-time.Duration(h)*time.Hour), 24, 0, "main", 300, 100_000)
	}
	// The last minutes: main fails a quarter of 40 slow calls costing
	// $20, backup 8 of 10.
	calls(t, w, now.Add(-time.Minute), 40, 10, "main", 2000, 500_000)
	calls(t, w, now.Add(-time.Minute), 10, 8, "backup", 300, 0)

	clock := now.Add(30 * time.Second)
	e := alerts.NewEvaluator(alerts.EvalConfig{Ent: w.App.Ent, Events: w.C.Events, Notifier: w.C.Notifier, Webhooks: w.C.Webhooks,
		Activity: w.App.Activity, Entitlements: w.C.Metering.Entitlements, Now: func() time.Time { return clock }})
	if err := e.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	state := func(name string) (bool, []string, float64) {
		t.Helper()
		r, err := s.Get(w.Scoped(), w.Project, ids[name])
		if err != nil {
			t.Fatal(err)
		}
		return r.Firing, r.FiringOn, r.LastValue
	}
	for name, want := range map[string]bool{"errors": true, "down": true, "slow": true, "spike": true, "quiet": false,
		"backup": false, "turned off": false} {
		if firing, on, v := state(name); firing != want {
			t.Errorf("%s: firing %v (%v, %.3f), want %v", name, firing, on, v, want)
		}
	}
	if _, on, _ := state("down"); len(on) != 1 || on[0] != "backup" {
		t.Fatalf("down on %v", on)
	}
	// errors: 18 of 50 calls failed.
	if _, _, v := state("errors"); v < 0.35 || v > 0.37 {
		t.Fatalf("error share %.3f", v)
	}
	told := func(kind string) (feed, hooks int) {
		t.Helper()
		f, _, err := w.App.Activity.List(w.Scoped(), w.Project, activity.Filter{Kind: kind}, nil, 50)
		if err != nil {
			t.Fatal(err)
		}
		return len(f), w.C.DB.Ent.WebhookDelivery.Query().Where(webhookdelivery.EventType(kind)).CountX(all)
	}
	if feed, hooks := told(alerts.EventFired.Name); feed != 4 || hooks != 4 {
		t.Fatalf("fired: %d entries, %d webhooks", feed, hooks)
	}

	// A minute later, still holding: nothing new until the cooldown.
	clock = clock.Add(time.Minute)
	if err := e.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if feed, _ := told(alerts.EventFired.Name); feed != 4 {
		t.Fatalf("fired again within the cooldown: %d", feed)
	}

	// Two hours later the windows are quiet: every rule resolves, once.
	clock = clock.Add(2 * time.Hour)
	for range 2 {
		if err := e.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if feed, hooks := told(alerts.EventResolved.Name); feed != 4 || hooks != 4 {
		t.Fatalf("resolved: %d entries, %d webhooks", feed, hooks)
	}
	if firing, _, _ := state("errors"); firing {
		t.Fatal("still firing")
	}
}

func TestCheck(t *testing.T) {
	for name, in := range map[string]alerts.Input{
		"a kind":              {Name: "x", Kind: "cpu"},
		"a window":            {Name: "x", Kind: alerts.ErrorRate, WindowMinutes: 7},
		"a spike's window":    {Name: "x", Kind: alerts.SpendSpike, WindowMinutes: 15},
		"a share":             {Name: "x", Kind: alerts.ErrorRate, Threshold: 2},
		"a latency":           {Name: "x", Kind: alerts.Latency, Threshold: 10},
		"min_micros":          {Name: "x", Kind: alerts.Latency, MinMicros: 5},
		"a name":              {Kind: alerts.ErrorRate},
		"a short cooldown":    {Name: "x", Kind: alerts.ErrorRate, CooldownMinutes: 1},
		"a spike's threshold": {Name: "x", Kind: alerts.SpendSpike, Threshold: 1},
	} {
		if err := in.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	in := alerts.Input{Name: "x", Kind: alerts.SpendSpike}
	if err := in.Check(); err != nil || in.Threshold != 3 || in.WindowMinutes != 60 || in.MinMicros != 5_000_000 || in.CooldownMinutes != 60 {
		t.Fatalf("defaults %+v %v", in, err)
	}
}
