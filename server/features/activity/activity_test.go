package activity_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/promptdam/server/features/activity"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
)

func TestFeed(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	ctx := context.Background()
	s := w.App.Activity
	for i, kind := range []string{"budget.threshold_reached", "budget.exceeded", "router.ready"} {
		subject := "bdg_1"
		if i == 2 {
			subject = "rtr_1"
		}
		s.Record(ctx, activity.Entry{Org: w.Org, Project: w.Project, Kind: kind, Title: kind, Subject: subject,
			Data: map[string]any{"n": i}})
	}
	// Newest first, in pages.
	page, next, err := s.List(w.Scoped(), w.Project, activity.Filter{}, uuid.Nil, 2)
	if err != nil || len(page) != 2 || next == uuid.Nil || page[0].Kind != "router.ready" || page[0].Severity != "info" {
		t.Fatalf("first page %+v %v", page, err)
	}
	if page, next, _ = s.List(w.Scoped(), w.Project, activity.Filter{}, next, 2); len(page) != 1 || next != uuid.Nil ||
		page[0].Kind != "budget.threshold_reached" || page[0].Data["n"] != float64(0) {
		t.Fatalf("second page %+v", page)
	}
	if h, _, _ := s.List(w.Scoped(), w.Project, activity.Filter{Subject: "bdg_1"}, uuid.Nil, 10); len(h) != 2 {
		t.Fatalf("a budget's history %+v", h)
	}
	if k, _, _ := s.List(w.Scoped(), w.Project, activity.Filter{Kind: "budget.exceeded"}, uuid.Nil, 10); len(k) != 1 {
		t.Fatalf("by kind %+v", k)
	}
	// Entries past the retention go.
	w.App.Ent.Activity.Create().SetOrgID(w.Org).SetProjectID(w.Project).SetKind("router.ready").SetTitle("old").
		SetCreatedAt(time.Now().Add(-activity.Retention - time.Hour)).ExecX(w.Scoped())
	if n, err := s.Prune(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if all, _, _ := s.List(w.Scoped(), w.Project, activity.Filter{}, uuid.Nil, 10); len(all) != 3 {
		t.Fatalf("left %d", len(all))
	}
}
