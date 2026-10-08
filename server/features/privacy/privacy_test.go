package privacy_test

import (
	"testing"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/payloads"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/routes"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

// A payload-free organization's gateways keep nothing, whatever its
// projects saved before.
func TestGatewaysKeepNothing(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	if _, err := w.App.Payloads.Put(w.Scoped(), w.Org, w.Project, payloads.Settings{Enabled: true, SampleRate: 1, Answers: true}); err != nil {
		t.Fatal(err)
	}
	fw := firewall.Defaults()
	fw.Excerpts = true
	if _, err := w.App.Firewall.Put(w.Scoped(), w.Org, w.Project, fw); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: routes.Default, CacheTTL: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Privacy.Put(w.Ctx, w.Org, true, "usr_test"); err != nil {
		t.Fatal(err)
	}
	const body = `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hello"}]}`
	for range 3 {
		if status, h, _ := w.Call("/v1/chat/completions", body); status != 200 || h.Get(brand.HeaderCache) != "" {
			t.Fatalf("%d, cache %q", status, h.Get(brand.HeaderCache))
		}
	}
	recs := w.Records()
	m := recs[len(recs)-1].Meta.(*resolver.Meta)
	if m.Capture != nil || recs[len(recs)-1].Body != nil {
		t.Fatalf("kept: capture %v, body %d bytes", m.Capture, len(recs[len(recs)-1].Body))
	}
	d, err := w.App.Sources().Build(w.Ctx, w.Org, w.Project)
	if err != nil {
		t.Fatal(err)
	}
	if snap := snapshot.Plain(w.Project, d); !snap.PayloadFree || snap.Capture != nil || snap.Firewall.Settings().Excerpts {
		t.Fatalf("the snapshot keeps content: %+v", snap)
	}
	if !w.App.Privacy.PayloadFree(w.Ctx, w.Org) {
		t.Fatal("not payload-free")
	}
}
