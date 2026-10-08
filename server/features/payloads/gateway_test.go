package payloads_test

import (
	"strings"
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/payloads"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
)

const chat = `{"model":"gpt-5-mini","messages":[{"role":"user","content":"mail ada@example.com"}]}`

// A project that keeps payloads gets each call's request and answer in
// its record, its errors' too; one that doesn't, nothing.
func TestTheGatewayKeepsCapturedCalls(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	w.Call("/v1/chat/completions", chat)
	if r := w.Records()[0]; r.Body != nil || r.Meta.(*resolver.Meta).Capture != nil {
		t.Fatalf("kept without capture: %s", r.Body)
	}

	w = gatewaytest.New(t, gatewaytest.Options{})
	if _, err := w.App.Payloads.Put(w.Scoped(), w.Org, w.Project, payloads.Settings{Enabled: true, SampleRate: 1, Answers: true}); err != nil {
		t.Fatal(err)
	}
	w.Call("/v1/chat/completions", chat)
	w.Fake.Fail(500, 500, 500)
	w.Call("/v1/chat/completions", chat)
	recs := w.Records()
	if len(recs) != 2 || recs[0].Meta.(*resolver.Meta).Capture == nil || !strings.Contains(string(recs[0].Body), "ada@example.com") ||
		!strings.Contains(string(recs[0].Answer), "chat.completion") {
		t.Fatalf("captured %+v", recs[0])
	}
	if recs[1].Status != 500 || !strings.Contains(string(recs[1].Answer), "fake failure") {
		t.Fatalf("the failed call: %d %s", recs[1].Status, recs[1].Answer)
	}

	// Tokenized calls aren't kept.
	s := firewall.Defaults()
	s.PII = firewall.Tokenize
	if _, err := w.App.Firewall.Put(w.Scoped(), w.Org, w.Project, s); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		w.Call("/v1/chat/completions", chat)
		recs := w.Records()
		return recs[len(recs)-1].Meta.(*resolver.Meta).Firewall.Tokens != nil
	})
	if recs := w.Records(); recs[len(recs)-1].Body != nil {
		t.Fatal("a tokenized call was kept")
	}
}
