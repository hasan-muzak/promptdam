package catalog_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stripe/stripe-go/v87"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
)

// `api stripe sync` against stripe-mock: Stripe accepts every price the
// catalog makes, the requests' metered prices (graduated, with fractions
// of a cent per request) included, each under the lookup key checkout asks
// for.
func TestCatalogSyncsToStripe(t *testing.T) {
	base := testkit.StripeMock(t)
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		URL: stripe.String(base), LeveledLogger: &stripe.LeveledLogger{Level: stripe.LevelNull},
	})
	g := billing.NewStripeGateway("sk_test_123", "whsec_test", brand.Key, stripe.WithBackends(backends))
	report, err := g.Sync(context.Background(), catalog.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	done := append(append(report.Created, report.Updated...), report.Kept...)
	for _, p := range catalog.Catalog.Plans {
		if p.Free() {
			continue
		}
		for _, interval := range []string{billing.Month, billing.Year} {
			keys := catalog.Catalog.CheckoutKeys(p, interval)
			if len(keys) != 2 {
				t.Fatalf("%s %s checks out %v: a base price and the requests' metered price", p.Key, interval, keys)
			}
			for _, k := range keys {
				if !slices.Contains(done, k) {
					t.Errorf("sync made no price %s (%+v)", k, report)
				}
			}
		}
	}
}
