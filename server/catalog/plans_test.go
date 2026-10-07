package catalog_test

import (
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/billing"

	"github.com/hasan-muzak/promptdam/server/catalog"
)

func TestCatalogMatchesThePricingTable(t *testing.T) {
	c := catalog.Catalog
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"free": 0, "pro": 4900, "team": 19900, "business": 49900}
	requests := map[string]int64{"free": 50_000, "pro": 500_000, "team": 3_000_000, "business": 10_000_000}
	retention := map[string]int64{"free": 7, "pro": 30, "team": 90, "business": 365}
	for _, p := range c.Plans {
		if p.Monthly != want[p.Key] {
			t.Errorf("%s costs %d cents, want %d", p.Key, p.Monthly, want[p.Key])
		}
		if p.Yearly != p.Monthly*10 {
			t.Errorf("%s yearly = %d: annual billing gets two months free", p.Key, p.Yearly)
		}
		if p.Limits[catalog.Requests] != requests[p.Key] || p.Limits[catalog.RetentionDays] != retention[p.Key] {
			t.Errorf("%s: %d requests kept %d days", p.Key, p.Limits[catalog.Requests], p.Limits[catalog.RetentionDays])
		}
		for _, m := range c.Meters {
			if _, ok := p.Limits[m.Key]; !ok {
				t.Errorf("%s has no limit for %s", p.Key, m.Key)
			}
		}
		// Secrets are blocked on every plan; the gateway is never paywalled.
		for _, f := range []string{catalog.Gateway, catalog.Fallbacks, catalog.ExactCache, catalog.SecretsBlocking} {
			if !p.Has(f) {
				t.Errorf("%s lacks %s", p.Key, f)
			}
		}
		wantRequests := billing.Soft
		if p.Free() {
			wantRequests = billing.Hard
		}
		if got := p.Policy(catalog.Requests); got != wantRequests {
			t.Errorf("%s requests policy = %s, want %s", p.Key, got, wantRequests)
		}
	}
	if free, _ := c.Plan("free"); free.Has(catalog.Budgets) {
		t.Error("budgets start on Pro")
	}

	pro, _ := c.Plan("pro")
	if got := billing.OverageCents(pro, catalog.Requests, 100_000); got != 500 {
		t.Fatalf("100k overage requests on Pro cost %d cents, want 500", got)
	}
	// Tokens are never billed: customers pay their providers.
	if len(c.Meters) != 1 || c.Meters[0].Key != catalog.Requests {
		t.Fatalf("meters %+v", c.Meters)
	}
	if pub := c.Public(); len(pub.Plans) != 4 || pub.Plans[0].Key != "free" || pub.Plans[3].Key != "business" {
		t.Fatalf("public plans = %+v", pub.Plans)
	}
}
