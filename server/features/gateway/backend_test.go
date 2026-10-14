package gateway_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// A call through OpenRouter asks it to say what the call cost, at the
// body's end (the prompt's bytes stay where they were), and the record
// keeps the backend that served it and that cost; a client's own usage
// setting is left alone, and other providers are asked nothing.
func TestOpenRouterBackendAndCost(t *testing.T) {
	route := func(kind string) func(string) gateway.Resolver {
		return func(base string) gateway.Resolver {
			return routeResolver{gateway.Route{Targets: []gateway.Target{{Provider: "openrouter", Kind: kind, BaseURL: base + "/v1",
				Model: "google/gemini-3.8-flash"}}}}
		}
	}
	e := setup(t, fakeupstream.Options{Backend: "Google AI Studio", Cost: 0.000206}, route("openrouter"))
	body := `{"model":"google/gemini-3.8-flash","messages":[{"role":"user","content":"hi"}]}`
	if res, _ := e.post(t, "/v1/chat/completions", body); res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	sent := e.fake.Requests()[0].Body
	if !bytes.HasPrefix(sent, []byte(strings.TrimSuffix(body, "}"))) || !bytes.HasSuffix(sent, []byte(`"usage":{"include":true}}`)) {
		t.Fatalf("sent %s", sent)
	}
	rec := e.record(t)
	if rec.Backend != "Google AI Studio" || !rec.Usage.CostReported || rec.Usage.Cost != 0.000206 {
		t.Fatalf("record backend %q, usage %+v", rec.Backend, rec.Usage)
	}

	e = setup(t, fakeupstream.Options{}, route("openrouter"))
	own := `{"model":"google/gemini-3.8-flash","usage":{"include":false},"messages":[{"role":"user","content":"hi"}]}`
	_, _ = e.post(t, "/v1/chat/completions", own)
	if got := string(e.fake.Requests()[0].Body); got != own {
		t.Fatalf("the client's usage setting changed: %s", got)
	}

	e = setup(t, fakeupstream.Options{}, route("openai"))
	_, _ = e.post(t, "/v1/chat/completions", body)
	if got := string(e.fake.Requests()[0].Body); got != body {
		t.Fatalf("another provider was asked for usage accounting: %s", got)
	}
}
