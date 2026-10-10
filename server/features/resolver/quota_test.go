package resolver_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/metering"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/responsecache"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

// The plan's requests and what Stripe is told of them (plans.md §3): the
// tests count most of a period's requests with metering.Add, and make the
// calls at the edge through the gateway.

// requests is the organization's live usage of its requests this period.
func requests(t *testing.T, w *gatewaytest.World) metering.MeterUsage {
	t.Helper()
	s, err := w.C.Metering.Usage(w.Ctx, w.Org)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range s.Meters {
		if m.Meter == catalog.Requests {
			return m
		}
	}
	t.Fatal("no requests meter")
	return metering.MeterUsage{}
}

func count(t *testing.T, w *gatewaytest.World, n int64) {
	t.Helper()
	if err := w.C.Metering.Add(w.Ctx, w.Org, catalog.Requests, n); err != nil {
		t.Fatal(err)
	}
}

// billed is what the fake Stripe was told of the organization's requests,
// each report once (as Stripe dedupes them).
func billed(t *testing.T, w *gatewaytest.World) int64 {
	t.Helper()
	if err := w.C.Metering.Flush(w.Ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.C.Metering.Rollup(w.Ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.C.Metering.Report(w.Ctx, nil); err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, e := range w.Stripe.Billed() {
		if e.CustomerID == w.Customer && e.EventName == catalog.Catalog.Meters[0].EventName {
			n += e.Value
		}
	}
	return n
}

// until is how a refusal names the end of the period.
func until(t *testing.T, w *gatewaytest.World) string {
	t.Helper()
	e, err := w.C.Billing.Entitlements(w.Ctx, w.Org)
	if err != nil {
		t.Fatal(err)
	}
	return "until " + e.PeriodEnd.UTC().Format("2006-01-02 15:04 UTC")
}

// Free stops at its 50,000 requests until the month ends. Calls refused
// before they go anywhere aren't counted; an answer from the response
// cache is.
func TestFreeStopsAtItsRequests(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: routes.Default, CacheTTL: 600}); err != nil {
		t.Fatal(err)
	}
	count(t, w, 49_997)

	// A wrong key and a secret are refused before they go anywhere.
	if status, _, body := w.Call("/v1/chat/completions", chat, "Authorization", "Bearer pd_sk_test_wrong"); status != 401 {
		t.Fatalf("wrong key: %d %s", status, body)
	}
	secret := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"deploy with AKIAIOSFODNN7EXAMPLE"}]}`
	if status, h, body := w.Call("/v1/chat/completions", secret); status != 400 || h.Get(brand.HeaderErrorCode) != "firewall_blocked" {
		t.Fatalf("secret: %d %s", status, body)
	}

	// Three calls fit: the second and third are the first's answer, from
	// the cache.
	if status, _, body := w.Call("/v1/chat/completions", chat); status != 200 {
		t.Fatalf("first: %d %s", status, body)
	}
	key, _ := responsecache.Key(w.Project.String()+":"+routes.Default, wire.OpenAIChat, []byte(chat))
	cache := responsecache.New(w.C.KV)
	w.Eventually(func() bool { _, hit := cache.Get(w.Ctx, key); return hit })
	for i := range 2 {
		if status, h, body := w.Call("/v1/chat/completions", chat); status != 200 || h.Get(brand.HeaderCache) != "hit" {
			t.Fatalf("cached %d: %d %s %s", i, status, h.Get(brand.HeaderCache), body)
		}
	}
	if u := requests(t, w); u.Used != 50_000 || u.Policy != string(billing.Hard) {
		t.Fatalf("usage %+v", u)
	}

	// The next is refused until the month ends, and not counted.
	status, h, body := w.Call("/v1/chat/completions", chat)
	if status != 429 || h.Get(brand.HeaderErrorCode) != "quota_exceeded" || !strings.Contains(body, until(t, w)) ||
		!strings.Contains(body, "upgrade") || h.Get("Retry-After") == "" {
		t.Fatalf("past the limit: %d %v %s", status, h, body)
	}
	if u := requests(t, w); u.Used != 50_000 {
		t.Fatalf("a refused call was counted: %d", u.Used)
	}
}

// A paid plan goes on past its requests, and Stripe is told every one:
// its metered price bills those past the plan's.
func TestPaidPlansGoOnAndReportTheirRequests(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{Stripe: true})
	w.Subscribe("pro")
	count(t, w, 599_999)
	if status, _, body := w.Call("/v1/chat/completions", chat); status != 200 {
		t.Fatalf("past Pro's requests: %d %s", status, body)
	}
	u := requests(t, w)
	if u.Used != 600_000 || u.Policy != string(billing.Soft) || u.Overage != 100_000 || u.OverageCents != 500 {
		t.Fatalf("usage %+v", u)
	}
	// The default spend cap is the plan's price: $49 of overage.
	if u.SpendCapCents != 4900 || u.Ceiling != 500_000+980_000 {
		t.Fatalf("cap %d, ceiling %d", u.SpendCapCents, u.Ceiling)
	}
	if n := billed(t, w); n != 600_000 {
		t.Fatalf("Stripe was told %d requests", n)
	}
	// Reported again, nothing new is sent.
	if n := billed(t, w); n != 600_000 {
		t.Fatalf("a second report sent %d", n)
	}
}

// The spend cap stops a paid plan's overage: calls past it are refused as
// Free's are, Stripe is never told more, and resetting the cap lets calls
// through again.
func TestTheSpendCapStopsOverage(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{Stripe: true})
	w.Subscribe("pro")
	// $1 of overage: 20,000 requests at $5 per 100k.
	if err := w.C.Billing.SetSpendCap(w.Ctx, w.Org, catalog.Requests, 100); err != nil {
		t.Fatal(err)
	}
	w.Fresh()
	count(t, w, 519_999)
	if status, _, body := w.Call("/v1/chat/completions", chat); status != 200 {
		t.Fatalf("at the cap: %d %s", status, body)
	}
	status, h, body := w.Call("/v1/chat/completions", chat)
	if status != 429 || h.Get(brand.HeaderErrorCode) != "spend_cap_reached" || !strings.Contains(body, until(t, w)) ||
		!strings.Contains(body, "raise the cap") {
		t.Fatalf("past the cap: %d %v %s", status, h, body)
	}
	// Usage counted past the cap some other way is never billed.
	count(t, w, 10)
	if n := billed(t, w); n != 520_000 {
		t.Fatalf("Stripe was told %d requests, want the cap's 520,000", n)
	}

	if err := w.C.Billing.ResetSpendCap(w.Ctx, w.Org, catalog.Requests); err != nil {
		t.Fatal(err)
	}
	w.Fresh()
	if status, _, body := w.Call("/v1/chat/completions", chat); status != 200 {
		t.Fatalf("after the reset: %d %s", status, body)
	}
}

// A cancelled plan stays until its period ends, with its features and its
// overage billed; then Free's limits and features apply, its requests
// counted afresh.
func TestACancelledPlanEndsWithItsPeriod(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{Stripe: true})
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-sonnet-5-5"})
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: routes.Default, PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	system, _ := json.Marshal(strings.Repeat("Follow the house style. ", 300))
	messages := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":` + string(system) + `,"messages":[{"role":"user","content":"hi"}]}`
	cached := func() bool {
		t.Helper()
		status, h, body := w.Call("/v1/messages", messages, "Authorization", "", "X-Api-Key", w.Raw)
		if status != 200 {
			t.Fatalf("%d %s", status, body)
		}
		return h.Get(brand.HeaderCache) == "prompt"
	}

	w.Subscribe("pro")
	if !cached() {
		t.Fatal("Pro adds no breakpoints")
	}
	count(t, w, 599_998)
	if err := w.C.Billing.Cancel(w.Ctx, w.Org); err != nil {
		t.Fatal(err)
	}
	w.SyncStripe()
	if e, _ := w.C.Billing.Entitlements(w.Ctx, w.Org); e.Plan != "pro" || !e.CancelAtPeriodEnd {
		t.Fatalf("cancelled: %+v", e)
	}
	if !cached() {
		t.Fatal("a cancelled plan lost its features before its period ended")
	}

	// The period ends: Stripe ends the subscription.
	w.Stripe.EndPeriod(w.Customer, time.Now().AddDate(0, 2, 0))
	w.SyncStripe()
	if cached() {
		t.Fatal("Free got breakpoints")
	}
	if u := requests(t, w); u.Used != 1 || u.Limit != 50_000 || u.Policy != string(billing.Hard) {
		t.Fatalf("Free's usage %+v", u)
	}
	// The ended period's requests are all reported.
	if n := billed(t, w); n != 600_000 {
		t.Fatalf("Stripe was told %d of the ended period's 600,000 requests", n)
	}
}
