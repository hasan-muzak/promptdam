package gateway_test

import (
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// An answer the gateway moved to another model (a router, a rule, a
// fallback) names the model the client asked for, whole or streamed,
// translated or not: agents keep state by the model they called.
// X-Promptdam-Served-Model names the other, and X-Promptdam-Answer-Model:
// served keeps it in the answer.
func TestAnswersNameTheModelAskedFor(t *testing.T) {
	moved := func(format wire.Format) func(string) gateway.Resolver {
		return func(base string) gateway.Resolver {
			return routeResolver{gateway.Route{Targets: []gateway.Target{{Provider: "p", BaseURL: base + "/v1", Model: "cheap-model",
				Format: format}}}}
		}
	}
	e := setup(t, fakeupstream.Options{}, moved(0))
	res, body := e.post(t, "/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, `"model":"auto"`) || strings.Contains(body, "cheap-model") ||
		res.Header.Get(brand.HeaderServedModel) != "cheap-model" {
		t.Fatalf("a whole answer: %s, served %q", body, res.Header.Get(brand.HeaderServedModel))
	}
	if rec := e.record(t); rec.ServedModel != "cheap-model" {
		t.Fatalf("the log keeps the truth: %+v", rec)
	}
	_, body = e.post(t, "/v1/chat/completions", `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, `"model":"auto"`) || strings.Contains(body, "cheap-model") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("a stream: %s", body)
	}
	_, body = e.post(t, "/v1/messages", `{"model":"auto","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, `"model":"auto"`) || strings.Contains(body, "cheap-model") {
		t.Fatalf("a Messages stream: %s", body)
	}
	_, body = e.post(t, "/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`,
		brand.HeaderAnswerModel, "served")
	if !strings.Contains(body, `"model":"cheap-model"`) {
		t.Fatalf("served asked for: %s", body)
	}

	// Translated, streamed.
	e = setup(t, fakeupstream.Options{}, moved(wire.AnthropicMessages))
	_, body = e.post(t, "/v1/chat/completions", `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, `"model":"auto"`) || strings.Contains(body, "cheap-model") {
		t.Fatalf("a translated stream: %s", body)
	}

	// A call the gateway didn't move keeps the provider's name (a dated
	// snapshot, say).
	e = setup(t, fakeupstream.Options{}, nil)
	_, body = e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, `"model":"gpt-5"`) {
		t.Fatalf("not moved: %s", body)
	}
}
