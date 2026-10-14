package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// provider is a test provider answering each call with fn, counting them.
func provider(t *testing.T, fn http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		fn(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// modelOf reads the model a call asks for.
func modelOf(r *http.Request) string {
	var body struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body.Model
}

// A call too long for one model goes on to the next target, a stronger
// tier's model among them; the last target's refusal reaches the client.
func TestTooLongCallsGoToTheNextTarget(t *testing.T) {
	up, _ := provider(t, func(w http.ResponseWriter, r *http.Request) {
		if modelOf(r) == "claude-haiku-4-5" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`)
	})
	e := setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return routeResolver{gateway.Route{Retries: 2, Targets: []gateway.Target{
			{ID: "fast", Provider: "anthropic", BaseURL: up.URL + "/v1", Model: "claude-haiku-4-5"},
			{ID: "strong", Provider: "anthropic", BaseURL: up.URL + "/v1", Model: "claude-sonnet-5-5"},
		}}}
	})
	res, body := e.post(t, "/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderServedModel) != "claude-sonnet-5-5" {
		t.Fatalf("%d %s, served %q", res.StatusCode, body, res.Header.Get(brand.HeaderServedModel))
	}
	// No retries of the model that can't take it.
	if rec := e.record(t); rec.Attempts != 2 || rec.ErrorCode != "" {
		t.Fatalf("record %+v", rec)
	}

	// Alone, the model's refusal is the client's answer.
	e = setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return gateway.Static{{Provider: "anthropic", BaseURL: up.URL + "/v1", Model: "claude-haiku-4-5"}}
	})
	res, body = e.post(t, "/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":5,"messages":[]}`)
	if res.StatusCode != 400 || !strings.Contains(body, "prompt is too long") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

// A short Retry-After is waited out and the same target asked again; a
// long one sends the call on, or to the client with the provider's
// Retry-After.
func TestRetryAfter(t *testing.T) {
	var limited atomic.Bool
	limited.Store(true)
	var waitAfter string
	up, calls := provider(t, func(w http.ResponseWriter, r *http.Request) {
		if limited.CompareAndSwap(true, false) {
			w.Header().Set("Retry-After", waitAfter)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","model":"gpt-5","choices":[]}`)
	})
	two := func(string) gateway.Resolver {
		return routeResolver{gateway.Route{Retries: 1, Targets: []gateway.Target{
			{ID: "a", Provider: "first", BaseURL: up.URL + "/v1"},
			{ID: "b", Provider: "second", BaseURL: up.URL + "/v1"},
		}}}
	}

	waitAfter = "0.2"
	e := setup(t, fakeupstream.Options{}, two)
	start := time.Now()
	res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderProvider) != "first" || time.Since(start) < 200*time.Millisecond {
		t.Fatalf("%d via %s after %v: the same target should answer after the wait", res.StatusCode,
			res.Header.Get(brand.HeaderProvider), time.Since(start))
	}

	limited.Store(true)
	waitAfter = "120"
	start = time.Now()
	res, _ = e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderProvider) != "second" || time.Since(start) > 2*time.Second {
		t.Fatalf("%d via %s after %v: the next target should answer at once", res.StatusCode,
			res.Header.Get(brand.HeaderProvider), time.Since(start))
	}

	// Alone, a long wait reaches the client, Retry-After and all.
	limited.Store(true)
	calls.Store(0)
	e = setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return routeResolver{gateway.Route{Retries: 2, Targets: []gateway.Target{{ID: "a", Provider: "first", BaseURL: up.URL + "/v1"}}}}
	})
	res, _ = e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	if res.StatusCode != 429 || res.Header.Get("Retry-After") != "120" || calls.Load() != 1 {
		t.Fatalf("%d, Retry-After %q, %d calls", res.StatusCode, res.Header.Get("Retry-After"), calls.Load())
	}
}

// A provider that got the whole call is never sent it again (it may be
// answering, and billing, still): the call goes on to the next target only.
// A provider that never got it is asked again.
func TestSentCallsAreNotSentTwice(t *testing.T) {
	hangup, got := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	fake := httptest.NewServer(fakeupstream.New(fakeupstream.Options{}))
	t.Cleanup(fake.Close)
	e := setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return routeResolver{gateway.Route{Retries: 2, Targets: []gateway.Target{
			{ID: "a", Provider: "hangup", BaseURL: hangup.URL + "/v1"},
			{ID: "b", Provider: "backup", BaseURL: fake.URL + "/v1"},
		}}}
	})
	res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderProvider) != "backup" || got.Load() != 1 {
		t.Fatalf("%d via %s, the first provider got %d calls", res.StatusCode, res.Header.Get(brand.HeaderProvider), got.Load())
	}

	// Alone, the client hears of it, and the provider still got one call.
	got.Store(0)
	e = setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return routeResolver{gateway.Route{Retries: 2, Targets: []gateway.Target{{ID: "a", Provider: "hangup", BaseURL: hangup.URL + "/v1"}}}}
	})
	res, _ = e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	if res.StatusCode != http.StatusBadGateway || res.Header.Get(brand.HeaderErrorCode) != "provider_disconnected" || got.Load() != 1 {
		t.Fatalf("%d %s, %d calls", res.StatusCode, res.Header.Get(brand.HeaderErrorCode), got.Load())
	}

	// A provider that can't be reached never got the call: it is asked
	// again.
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String()
	_ = ln.Close()
	e = setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return routeResolver{gateway.Route{Retries: 2, Targets: []gateway.Target{{ID: "a", Provider: "gone", BaseURL: closed + "/v1"}}}}
	})
	if res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`); res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", res.StatusCode)
	}
	if rec := e.record(t); rec.Attempts != 3 || rec.ErrorCode != "provider_unreachable" {
		t.Fatalf("record %+v", rec)
	}
}

// An overload reported inside a stream that began fine counts against the
// provider's circuit: after five, calls go to the fallback.
func TestStreamErrorsCountAgainstTheCircuit(t *testing.T) {
	overloaded, _ := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-haiku-4-5\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"+
			"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	})
	fake := httptest.NewServer(fakeupstream.New(fakeupstream.Options{}))
	t.Cleanup(fake.Close)
	e := setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return routeResolver{gateway.Route{Targets: []gateway.Target{
			{ID: "busy", Provider: "busy", BaseURL: overloaded.URL + "/v1"},
			{ID: "up", Provider: "up", BaseURL: fake.URL + "/v1"},
		}}}
	})
	call := `{"model":"claude-haiku-4-5","max_tokens":5,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	for i := range 5 {
		res, _ := e.post(t, "/v1/messages", call)
		if res.Header.Get(brand.HeaderProvider) != "busy" {
			t.Fatalf("call %d went to %s", i, res.Header.Get(brand.HeaderProvider))
		}
	}
	if res, _ := e.post(t, "/v1/messages", call); res.Header.Get(brand.HeaderProvider) != "up" {
		t.Fatalf("after five overloads the call went to %s", res.Header.Get(brand.HeaderProvider))
	}
}

// A call one target's API can't express goes on to the next target at
// once: translating it again would fail the same way.
func TestUntranslatableCallsMoveOn(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return routeResolver{gateway.Route{Retries: 2, Targets: []gateway.Target{
			{ID: "a", Provider: "anthropic", BaseURL: base + "/v1", Model: "claude-haiku-4-5", Format: wire.AnthropicMessages},
			{ID: "b", Provider: "openai", BaseURL: base + "/v1"},
		}}}
	})
	// Several answers (n) have no Messages equivalent.
	res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","n":2,"messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderProvider) != "openai" {
		t.Fatalf("%d via %s", res.StatusCode, res.Header.Get(brand.HeaderProvider))
	}
	if rec := e.record(t); rec.Attempts != 2 {
		t.Fatalf("record %+v", rec)
	}
}

// The prompt cache's header speaks for the attempt that answered, not one
// that failed before it.
func TestPromptCacheHeaderIsTheAnsweringAttempts(t *testing.T) {
	var failed atomic.Bool
	up, _ := provider(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") && failed.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fakeupstream.New(fakeupstream.Options{}).ServeHTTP(w, r)
	})
	e := setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return routeResolver{gateway.Route{PromptCache: true, Targets: []gateway.Target{
			{ID: "a", Provider: "anthropic", BaseURL: up.URL + "/v1", Model: "claude-sonnet-5-5", Format: wire.AnthropicMessages},
			{ID: "b", Provider: "openai", BaseURL: up.URL + "/v1"},
		}}}
	})
	system := strings.Repeat("You answer questions about the product. ", 200)
	res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"system","content":"`+system+`"},{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderProvider) != "openai" || res.Header.Get(brand.HeaderCache) == "prompt" {
		t.Fatalf("%d via %s, cache %q", res.StatusCode, res.Header.Get(brand.HeaderProvider), res.Header.Get(brand.HeaderCache))
	}
}
