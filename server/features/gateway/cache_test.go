package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// Routes with the prompt cache: Anthropic calls get breakpoints for their
// prefix and, for a conversation, its tail; OpenAI calls of a conversation
// get its key; the client's own win.
func TestPromptCaching(t *testing.T) {
	route := func(target gateway.Target, key string) func(string) gateway.Resolver {
		return func(base string) gateway.Resolver {
			target.BaseURL = base + "/v1"
			return routeResolver{gateway.Route{PromptCache: true, PromptKey: key, Targets: []gateway.Target{target}}}
		}
	}
	system, _ := json.Marshal(strings.Repeat("Follow the house style. ", 120)) // ~720 tokens
	conversation := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":` + string(system) + `,"messages":[
		{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	e := setup(t, fakeupstream.Options{}, route(gateway.Target{Provider: "anthropic", Kind: "anthropic", MinCache: 512}, ""))
	res, _ := e.post(t, "/v1/messages", conversation)
	body := lastBody(t, e)
	sys, _ := body["system"].([]any)
	if body["cache_control"] == nil || len(sys) != 1 || res.Header.Get(brand.HeaderCache) != "prompt" {
		t.Fatalf("a conversation: %v", body)
	}
	// One question, no tools: its tail is never read again.
	question := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":` + string(system) + `,"messages":[{"role":"user","content":"a"}]}`
	_, _ = e.post(t, "/v1/messages", question)
	if body := lastBody(t, e); body["cache_control"] != nil || body["system"] == nil {
		t.Fatalf("a question: %v", body)
	}

	// OpenAI: the conversation's key, unless the client set one.
	e = setup(t, fakeupstream.Options{}, route(gateway.Target{Provider: "openai", Kind: "openai"}, "pd-conv"))
	_, _ = e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"a"}]}`)
	if lastBody(t, e)["prompt_cache_key"] != "pd-conv" {
		t.Fatalf("openai: %v", lastBody(t, e))
	}
	_, _ = e.post(t, "/v1/chat/completions", `{"model":"gpt-5","prompt_cache_key":"mine","messages":[{"role":"user","content":"a"}]}`)
	if lastBody(t, e)["prompt_cache_key"] != "mine" {
		t.Fatalf("the client's key: %v", lastBody(t, e))
	}

	// A call translated to an Anthropic model is marked for that model:
	// its least cacheable prefix, not the one of the model asked for.
	e = setup(t, fakeupstream.Options{}, route(gateway.Target{Provider: "anthropic", Kind: "anthropic", Model: "claude-opus-5-5",
		Format: wire.AnthropicMessages, MinCache: 512}, ""))
	_, _ = e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"system","content":`+string(system)+`},
		{"role":"user","content":"a"}]}`)
	if sys, _ := lastBody(t, e)["system"].([]any); len(sys) != 1 || sys[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("translated: %v", lastBody(t, e))
	}
}

// A thinking block a firewall rewrite invalidated is healed once (the
// provider asked to drop it); a call the gateway didn't rewrite gets the
// provider's refusal, its history being the client's to fix.
func TestThinkingIsHealedAfterARewrite(t *testing.T) {
	var calls atomic.Int32
	var beta, binding atomic.Value
	up, _ := provider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(string(body), "drop_block") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid `+"`signature`"+` in `+"`thinking`"+` block. The block is bound to a different conversation."}}`)
			return
		}
		beta.Store(r.Header.Get("Anthropic-Beta"))
		binding.Store(string(body))
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`)
	})
	call := `{"model":"claude-opus-5-5","max_tokens":9,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"[REDACTED:email]"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"s"},{"type":"text","text":"b"}]},{"role":"user","content":"c"}]}`
	rewritten := func(string) gateway.Resolver {
		return routeResolver{gateway.Route{Body: []byte(call), Targets: []gateway.Target{{Provider: "anthropic", Kind: "anthropic",
			BaseURL: up.URL + "/v1"}}}}
	}
	e := setup(t, fakeupstream.Options{}, rewritten)
	res, out := e.post(t, "/v1/messages", call, "Anthropic-Beta", "files-api")
	if res.StatusCode != 200 || calls.Load() != 2 || !strings.Contains(beta.Load().(string), "thinking-binding-controls-2026-08-01") ||
		!strings.Contains(beta.Load().(string), "files-api") {
		t.Fatalf("%d %s, %d calls, beta %v", res.StatusCode, out, calls.Load(), beta.Load())
	}
	if rec := e.record(t); rec.Healed != gateway.HealDropBlock {
		t.Fatalf("record %+v", rec)
	}

	calls.Store(0)
	e = setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return gateway.Static{{Provider: "anthropic", Kind: "anthropic", BaseURL: up.URL + "/v1"}}
	})
	if res, _ := e.post(t, "/v1/messages", call); res.StatusCode != 400 || calls.Load() != 1 {
		t.Fatalf("not rewritten: %d, %d calls", res.StatusCode, calls.Load())
	}
}
