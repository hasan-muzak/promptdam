package gateway_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
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

// OpenRouter calls carry the conversation's session from its first call,
// so OpenRouter keeps it on one backend; a model that caches what a
// request marks gets breakpoints on its Chat content (calls translated to
// it too), one that caches on its own gets none; a session the client
// chose wins.
func TestOpenRouterCaching(t *testing.T) {
	route := func(strategy, session string) func(string) gateway.Resolver {
		return func(base string) gateway.Resolver {
			return routeResolver{gateway.Route{PromptCache: true, Session: session, Targets: []gateway.Target{{Provider: "openrouter",
				Kind: "openrouter", BaseURL: base + "/v1", Model: "google/gemini-3.8-flash", MinCache: 512,
				Cache: gateway.CachePlan{Strategy: strategy}}}}}
		}
	}
	system, _ := json.Marshal(strings.Repeat("Follow the house style. ", 120)) // ~720 tokens
	chat := `{"model":"google/gemini-3.8-flash","messages":[{"role":"system","content":` + string(system) + `},
		{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	sent := func(e *env) (map[string]any, http.Header) {
		t.Helper()
		reqs := e.fake.Requests()
		var body map[string]any
		_ = json.Unmarshal(reqs[len(reqs)-1].Body, &body)
		return body, reqs[len(reqs)-1].Header
	}
	marked := func(msg any) bool {
		parts, _ := msg.(map[string]any)["content"].([]any)
		if len(parts) == 0 {
			return false
		}
		return parts[len(parts)-1].(map[string]any)["cache_control"] != nil
	}

	e := setup(t, fakeupstream.Options{}, route("markers", "pd-conv"))
	res, _ := e.post(t, "/v1/chat/completions", chat)
	body, h := sent(e)
	msgs, _ := body["messages"].([]any)
	if h.Get("X-Session-Id") != "pd-conv" || len(msgs) != 4 || !marked(msgs[0]) || marked(msgs[1]) || !marked(msgs[3]) ||
		res.Header.Get(brand.HeaderCache) != "prompt" {
		t.Fatalf("markers: session %q, body %v", h.Get("X-Session-Id"), body)
	}
	if rec := e.record(t); rec.CacheStrategy != "markers" {
		t.Fatalf("record %+v", rec)
	}

	// A Messages call translated to OpenRouter's Chat is marked as Chat.
	translated := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return routeResolver{gateway.Route{PromptCache: true, Targets: []gateway.Target{{Provider: "openrouter", Kind: "openrouter",
			BaseURL: base + "/v1", Model: "google/gemini-3.8-flash", Format: wire.OpenAIChat, MinCache: 512,
			Cache: gateway.CachePlan{Strategy: "markers"}}}}}
	})
	messages := `{"model":"claude-x","max_tokens":9,"system":` + string(system) + `,"messages":[{"role":"user","content":"a"}]}`
	_, _ = translated.post(t, "/v1/messages", messages)
	if body, _ := sent(translated); !marked(body["messages"].([]any)[0]) {
		t.Fatalf("translated: %v", body)
	}

	e = setup(t, fakeupstream.Options{}, route("auto", "pd-conv"))
	_, _ = e.post(t, "/v1/chat/completions", chat)
	if body, h := sent(e); h.Get("X-Session-Id") != "pd-conv" || strings.Contains(fmtJSON(body), "cache_control") {
		t.Fatalf("auto: session %q, body %v", h.Get("X-Session-Id"), body)
	}

	// The client's session: its own header, or session_id in the body.
	_, _ = e.post(t, "/v1/chat/completions", chat, "X-Session-Id", "theirs")
	if _, h := sent(e); h.Get("X-Session-Id") != "theirs" {
		t.Fatalf("the client's header: %q", h.Get("X-Session-Id"))
	}
	_, _ = e.post(t, "/v1/chat/completions", strings.Replace(chat, `"messages"`, `"session_id":"s1","messages"`, 1))
	if _, h := sent(e); h.Get("X-Session-Id") != "" {
		t.Fatalf("a body session_id got a header too: %q", h.Get("X-Session-Id"))
	}
}

// A provider that refuses the gateway's breakpoints gets the call again
// unmarked and without the gateway's session; when that answers, markers
// are switched off for the model where its kind may cache without them.
// A retry refused too, or Anthropic's refusal of a call's own breakpoints,
// switches nothing off.
func TestRefusedMarkersAreDropped(t *testing.T) {
	system, _ := json.Marshal(strings.Repeat("Follow the house style. ", 120))
	chat := `{"model":"google/gemini-3.8-flash","messages":[{"role":"system","content":` + string(system) + `},
		{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	refusal := `{"error":{"message":"Unknown parameter: 'messages[0].content[0].cache_control'.","code":400}}`
	answer := `{"id":"c","object":"chat.completion","model":"google/gemini-3.8-flash","choices":[{"index":0,
		"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`
	type seen struct {
		marked  bool
		session string
	}
	var mu sync.Mutex
	var calls []seen
	refuse := func(always bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			calls = append(calls, seen{bytes.Contains(body, []byte("cache_control")), r.Header.Get("X-Session-Id")})
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if always || bytes.Contains(body, []byte("cache_control")) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, refusal)
				return
			}
			_, _ = io.WriteString(w, answer)
		}
	}
	type kill struct{ kind, model, strategy string }
	var kills []kill
	run := func(handler http.HandlerFunc, kind string) (int, gateway.Record) {
		t.Helper()
		calls, kills = nil, nil
		up, _ := provider(t, handler)
		e := setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
			return routeResolver{gateway.Route{PromptCache: true, Session: "pd-conv", Targets: []gateway.Target{{Provider: kind,
				Kind: kind, BaseURL: up.URL + "/v1", Model: "google/gemini-3.8-flash", MinCache: 512,
				Cache: gateway.CachePlan{Strategy: "markers"}}}}}
		}, func(c *gateway.Config) {
			c.CacheRefused = func(kind, model, strategy string) { kills = append(kills, kill{kind, model, strategy}) }
		})
		res, _ := e.post(t, "/v1/chat/completions", chat)
		return res.StatusCode, e.record(t)
	}

	status, rec := run(refuse(false), "openrouter")
	if status != 200 || len(calls) != 2 || !calls[0].marked || calls[0].session != "pd-conv" || calls[1].marked ||
		calls[1].session != "" {
		t.Fatalf("%d, calls %+v", status, calls)
	}
	if rec.Healed != gateway.HealUnmarked || rec.PromptCached || len(kills) != 1 ||
		kills[0] != (kill{"openrouter", "google/gemini-3.8-flash", "markers"}) {
		t.Fatalf("record %+v, kills %+v", rec, kills)
	}

	if status, _ := run(refuse(true), "openrouter"); status != 400 || len(calls) != 2 || len(kills) != 0 {
		t.Fatalf("refused again: %d, %d calls, kills %+v", status, len(calls), kills)
	}

	// A custom endpoint may cache on its own: it loses its markers too.
	if status, _ := run(refuse(false), "custom"); status != 200 || len(kills) != 1 {
		t.Fatalf("custom: %d, kills %+v", status, kills)
	}

	// Anthropic takes breakpoints: a refusal names a call's own.
	up, n := provider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(body, []byte("cache_control")) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"A maximum of 4 blocks with cache_control may be provided. Found 5."}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`)
	})
	kills = nil
	e := setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return routeResolver{gateway.Route{PromptCache: true, Targets: []gateway.Target{{Provider: "anthropic", Kind: "anthropic",
			BaseURL: up.URL + "/v1", MinCache: 512}}}}
	}, func(c *gateway.Config) {
		c.CacheRefused = func(kind, model, strategy string) { kills = append(kills, kill{kind, model, strategy}) }
	})
	res, out := e.post(t, "/v1/messages", `{"model":"claude-opus-5-5","max_tokens":9,"system":`+string(system)+`,
		"messages":[{"role":"user","content":"a"}]}`)
	if res.StatusCode != 200 || n.Load() != 2 || len(kills) != 0 || e.record(t).Healed != gateway.HealUnmarked {
		t.Fatalf("anthropic: %d %s, %d calls, kills %+v", res.StatusCode, out, n.Load(), kills)
	}

	// A refusal that doesn't name the gateway's caching is the client's.
	up, n = provider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"max_tokens is too large","code":400}}`)
	})
	e = setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return routeResolver{gateway.Route{PromptCache: true, Session: "pd-conv", Targets: []gateway.Target{{Provider: "openrouter",
			Kind: "openrouter", BaseURL: up.URL + "/v1", Model: "google/gemini-3.8-flash", MinCache: 512,
			Cache: gateway.CachePlan{Strategy: "markers"}}}}}
	})
	if res, _ := e.post(t, "/v1/chat/completions", chat); res.StatusCode != 400 || n.Load() != 1 {
		t.Fatalf("not ours: %d, %d calls", res.StatusCode, n.Load())
	}
}

func fmtJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
