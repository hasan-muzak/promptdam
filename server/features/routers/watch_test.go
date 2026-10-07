package routers_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

// judge writes judged calls of a router: n of the tier's answers, held up
// or not, on calls the tier served or the default tier did.
func judge(t *testing.T, w *gatewaytest.World, n int, served, tier, verdict string) {
	t.Helper()
	ctx := context.Background()
	batch, err := w.C.Events.Unscoped().PrepareBatch(ctx, `INSERT INTO router_evals (project_id, ts, org_id, router,
		served_tier, baseline_tier, candidate_tier, candidate_rank, verdict, check, retention_days)`)
	if err != nil {
		t.Fatal(err)
	}
	for range n {
		if err := batch.Append(w.Project, time.Now(), w.Org, "auto", served, "smart", tier, int8(-1), verdict, "judge", uint16(7)); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}
}

func spec() tiers.Spec {
	return tiers.Spec{Tiers: []tiers.Tier{{Name: "fast", Model: "gpt-5-mini"}, {Name: "smart", Model: "gpt-5"}},
		Rules: []tiers.Rule{{When: "request.messages == 1", Tier: "fast"}}}
}

// A shadow router whose tiers hold up is announced once.
func TestWatcherAnnouncesReadyRouters(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Sample: func() float64 { return 1 }})
	ctx := context.Background()
	row, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Shadow, Spec: spec()})
	if err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		_, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
		return h.Get(brand.HeaderTierSource) == "shadow:rule"
	})
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	judge(t, w, 40, "smart", "fast", "tie")
	if err := w.App.Watcher.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := w.App.Routers.Get(w.Scoped(), w.Project, row.ID); r.ReadyAt != nil {
		t.Fatal("ready on 40 judged calls")
	}
	judge(t, w, 50, "smart", "fast", "tie")
	if err := w.App.Watcher.Check(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := w.App.Routers.Get(w.Scoped(), w.Project, row.ID)
	if r.ReadyAt == nil {
		rep, err := routers.BuildReport(ctx, w.C.Events, w.Project, routers.Of(r), time.Now().Add(-time.Hour), time.Now(), nil)
		t.Fatalf("not ready on 90 judged calls that held up: %+v %v", rep, err)
	}
	at := *r.ReadyAt
	if err := w.App.Watcher.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := w.App.Routers.Get(w.Scoped(), w.Project, row.ID); !again.ReadyAt.Equal(at) {
		t.Fatal("announced twice")
	}
}

// An active router's cheaper tier that falls below the floor on the calls
// it served is demoted: its calls go to the next stronger tier until the
// project restores it.
func TestWatcherDemotesDriftingTiers(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Sample: func() float64 { return 1 }})
	ctx := context.Background()
	row, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "auto", Status: tiers.Active, Spec: spec()})
	if err != nil {
		t.Fatal(err)
	}
	call := func() string {
		_, h, _ := w.Call("/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
		return h.Get(brand.HeaderTier)
	}
	w.Eventually(func() bool { return call() == "fast" })

	// Judged on calls the default tier served, the fast tier's quality
	// says nothing of what the router does: no demotion.
	judge(t, w, 60, "smart", "fast", "loss")
	if err := w.App.Watcher.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := w.App.Routers.Get(w.Scoped(), w.Project, row.ID); len(r.Demoted) != 0 {
		t.Fatalf("demoted on calls it didn't serve: %+v", r.Demoted)
	}
	// On calls it served: 40 of 60 held up, far under 95%.
	judge(t, w, 40, "fast", "fast", "tie")
	judge(t, w, 20, "fast", "fast", "loss")
	if err := w.App.Watcher.Check(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := w.App.Routers.Get(w.Scoped(), w.Project, row.ID)
	if len(r.Demoted) != 1 || r.Demoted[0].Tier != "fast" || r.Demoted[0].Judged != 60 || r.Demoted[0].Right != 40 {
		t.Fatalf("demoted %+v", r.Demoted)
	}
	w.Eventually(func() bool { return call() == "smart" })
	// A client may still name it.
	if _, h, _ := w.Call("/v1/chat/completions", `{"model":"auto:fast","messages":[{"role":"user","content":"hi"}]}`); h.Get(brand.HeaderTier) != "fast" {
		t.Fatalf("named: %s", h.Get(brand.HeaderTier))
	}

	if _, err := w.App.Routers.Restore(w.Scoped(), w.Project, row.ID, "fast"); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool { return call() == "fast" })
	if _, err := w.App.Routers.Restore(w.Scoped(), w.Project, uuid.New(), "fast"); err == nil {
		t.Fatal("restored a tier of no router")
	}
}
