package gateway_test

import (
	"encoding/json"
	"testing"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// lastBody is the last request body the fake provider got, decoded.
func lastBody(t *testing.T, e *env) map[string]any {
	t.Helper()
	reqs := e.fake.Requests()
	if len(reqs) == 0 {
		t.Fatal("the provider got no call")
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// A router tier's reasoning effort reaches the provider in its own words,
// and the client is told; the model's default needn't be written.
func TestTierEffortReachesTheProvider(t *testing.T) {
	route := func(target gateway.Target) func(string) gateway.Resolver {
		return func(base string) gateway.Resolver {
			target.BaseURL = base + "/v1"
			return routeResolver{gateway.Route{Targets: []gateway.Target{target}}}
		}
	}
	e := setup(t, fakeupstream.Options{}, route(gateway.Target{Provider: "openai", Kind: "openai", Effort: "low", DefaultEffort: "medium"}))
	res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderEffort) != "low" || lastBody(t, e)["reasoning_effort"] != "low" {
		t.Fatalf("%d, effort %q, body %v", res.StatusCode, res.Header.Get(brand.HeaderEffort), lastBody(t, e))
	}
	if rec := e.record(t); rec.Effort != "low" {
		t.Fatalf("record %+v", rec)
	}

	// Anthropic: output_config.effort, beside what the client set there.
	e = setup(t, fakeupstream.Options{}, route(gateway.Target{Provider: "anthropic", Kind: "anthropic", Effort: "medium", DefaultEffort: "high"}))
	res, _ = e.post(t, "/v1/messages", `{"model":"claude-sonnet-5-5","max_tokens":9,"output_config":{"format":{"type":"json_schema","schema":{}}},
		"messages":[{"role":"user","content":"hi"}]}`)
	oc, _ := lastBody(t, e)["output_config"].(map[string]any)
	if res.StatusCode != 200 || oc["effort"] != "medium" || oc["format"] == nil {
		t.Fatalf("%d, output_config %v", res.StatusCode, oc)
	}

	// The model's default, which the client didn't change: nothing to
	// write (the cache keys on it).
	e = setup(t, fakeupstream.Options{}, route(gateway.Target{Provider: "anthropic", Kind: "anthropic", Effort: "medium", DefaultEffort: "medium"}))
	res, _ = e.post(t, "/v1/messages", `{"model":"claude-opus-5-5","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`)
	if _, set := lastBody(t, e)["output_config"]; set || res.Header.Get(brand.HeaderEffort) != "" {
		t.Fatalf("the default was written: %v", lastBody(t, e))
	}

	// A provider with no known way to take one: the call goes as it is,
	// and the client is told the effort was dropped.
	e = setup(t, fakeupstream.Options{}, route(gateway.Target{Provider: "mistral", Kind: "mistral", Effort: "low"}))
	res, _ = e.post(t, "/v1/chat/completions", `{"model":"magistral","messages":[{"role":"user","content":"hi"}]}`)
	if _, set := lastBody(t, e)["reasoning_effort"]; set || res.Header.Get(brand.HeaderDropped) != "effort" {
		t.Fatalf("dropped %q, body %v", res.Header.Get(brand.HeaderDropped), lastBody(t, e))
	}

	// A call moved to a model known not to reason loses the client's
	// effort, meant for the model it asked for.
	e = setup(t, fakeupstream.Options{}, route(gateway.Target{Provider: "openai", Kind: "openai", Model: "gpt-4.1", NoReasoning: true}))
	_, _ = e.post(t, "/v1/chat/completions", `{"model":"gpt-5","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	if _, set := lastBody(t, e)["reasoning_effort"]; set {
		t.Fatalf("a model that doesn't reason got an effort: %v", lastBody(t, e))
	}
}
