package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasan-muzak/keel/pkg/llm"
	"github.com/hasan-muzak/keel/pkg/llm/wire"
	"github.com/hasan-muzak/keel/pkg/safehttp"

	"github.com/hasan-muzak/keel/products/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/gateway"
)

// env is a gateway in front of a fake provider.
type env struct {
	fake     *fakeupstream.Server
	upstream *httptest.Server
	gateway  *httptest.Server

	mu      sync.Mutex
	records []gateway.Record
}

func setup(t *testing.T, opts fakeupstream.Options, resolver func(base string) gateway.Resolver, cfg ...func(*gateway.Config)) *env {
	t.Helper()
	e := &env{fake: fakeupstream.New(opts)}
	e.upstream = httptest.NewServer(e.fake)
	t.Cleanup(e.upstream.Close)
	if resolver == nil {
		resolver = func(base string) gateway.Resolver {
			return gateway.Static{{Provider: "openai", BaseURL: base + "/v1", Header: http.Header{"Authorization": {"Bearer sk-provider"}}}}
		}
	}
	c := gateway.Config{
		Resolver:  resolver(e.upstream.URL),
		Transport: safehttp.NewTransport(safehttp.TransportOptions{AllowLoopback: true}),
		Done: func(r *gateway.Record) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.records = append(e.records, *r)
		},
	}
	for _, f := range cfg {
		f(&c)
	}
	e.gateway = httptest.NewServer(gateway.New(c))
	t.Cleanup(e.gateway.Close)
	return e
}

// reply is a response read in full.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (e *env) post(t *testing.T, path, body string, header ...string) (reply, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.gateway.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer pd_sk_test_client")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return reply{StatusCode: res.StatusCode, Header: res.Header}, string(out)
}

// record waits for the last call's record: Done runs when the handler
// returns, which may be just after the client read the whole body.
func (e *env) record(t *testing.T) gateway.Record {
	t.Helper()
	for range 200 {
		e.mu.Lock()
		if n := len(e.records); n > 0 {
			r := e.records[n-1]
			e.mu.Unlock()
			return r
		}
		e.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no record")
	return gateway.Record{}
}

func openAIUsage() wire.Usage {
	return wire.Usage{Usage: llm.Usage{Input: fakeupstream.PromptTokens, CacheRead: fakeupstream.CachedTokens, Output: 10}}
}

func TestChatCompletion(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, nil)
	res, body := e.post(t, "/v1/chat/completions", `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 200 || !strings.Contains(body, `"content":"tok0 tok1`) {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if res.Header.Get(brand.HeaderTrace) == "" || res.Header.Get(brand.HeaderProvider) != "openai" ||
		res.Header.Get(brand.HeaderServedModel) != "gpt-5-mini" || res.Header.Get("X-Request-Id") != "req_fake" {
		t.Fatalf("headers %v", res.Header)
	}
	rec := e.record(t)
	if !rec.UsageKnown || rec.Usage != openAIUsage() || rec.ServedModel != "gpt-5-mini" || rec.Status != 200 ||
		rec.ErrorCode != "" || rec.Attempts != 1 || rec.Stream || rec.Trace != res.Header.Get(brand.HeaderTrace) {
		t.Fatalf("record %+v", rec)
	}
	up := e.fake.Requests()[0]
	// The provider gets the provider's key, never the client's.
	if up.Path != "/chat/completions" || up.Header.Get("Authorization") != "Bearer sk-provider" ||
		up.Header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("upstream request %s %v", up.Path, up.Header)
	}
}

func TestChatStreamGetsUsageWithoutShowingIt(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, nil)
	_, body := e.post(t, "/v1/chat/completions", `{"model":"gpt-5-mini","stream":true,"messages":[]}`)
	if !strings.HasSuffix(body, "data: [DONE]\n\n") || strings.Contains(body, `"usage":{`) {
		t.Fatalf("the client sees the usage chunk it didn't ask for:\n%s", body)
	}
	if rec := e.record(t); !rec.UsageKnown || rec.Usage != openAIUsage() || !rec.Stream || rec.TTFB <= 0 {
		t.Fatalf("record %+v", rec)
	}
	if up := e.fake.Requests()[0]; !bytes.Contains(up.Body, []byte(`"stream_options":{"include_usage":true}`)) {
		t.Fatalf("usage wasn't asked for: %s", up.Body)
	}

	// A client that asks for usage gets it.
	_, body = e.post(t, "/v1/chat/completions", `{"model":"gpt-5-mini","stream":true,"stream_options":{"include_usage":true},"messages":[]}`)
	if !strings.Contains(body, `"usage":{`) {
		t.Fatalf("the usage chunk is missing:\n%s", body)
	}
}

func TestAnthropicStreamIsForwardedByteForByte(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return gateway.Static{{Provider: "anthropic", BaseURL: base + "/v1", Header: http.Header{"X-Api-Key": {"sk-ant-provider"}}}}
	})
	req := `{"model":"claude-sonnet-5-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	_, body := e.post(t, "/v1/messages", req, "X-Api-Key", "pd_sk_test_client", "Anthropic-Beta", "prompt-caching-2024-07-31")

	directReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.upstream.URL+"/v1/messages", strings.NewReader(req))
	direct, err := http.DefaultClient.Do(directReq)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := io.ReadAll(direct.Body)
	_ = direct.Body.Close()
	if body != string(want) {
		t.Fatalf("the stream changed on the way:\n%s\nwant\n%s", body, want)
	}
	rec := e.record(t)
	wantUsage := wire.Usage{Usage: llm.Usage{
		Input:     fakeupstream.PromptTokens + fakeupstream.CacheReadTokens + fakeupstream.CacheWriteTokens,
		CacheRead: fakeupstream.CacheReadTokens, CacheWrite: fakeupstream.CacheWriteTokens, Output: 10,
	}}
	if !rec.UsageKnown || rec.Usage != wantUsage || rec.Format != wire.AnthropicMessages {
		t.Fatalf("record %+v", rec)
	}
	up := e.fake.Requests()[0]
	if up.Header.Get("X-Api-Key") != "sk-ant-provider" || up.Header.Get("Anthropic-Version") != "2023-06-01" ||
		up.Header.Get("Anthropic-Beta") != "prompt-caching-2024-07-31" || up.Header.Get("Authorization") != "" {
		t.Fatalf("upstream headers %v", up.Header)
	}
}

func TestOtherEndpoints(t *testing.T) {
	tests := []struct {
		path, body string
		usage      wire.Usage
		known      bool
	}{
		{"/v1/responses", `{"model":"gpt-5","input":"hi"}`, openAIUsage(), true},
		{"/v1/responses", `{"model":"gpt-5","input":"hi","stream":true}`, openAIUsage(), true},
		{"/v1/embeddings", `{"model":"text-embedding-3-small","input":"hi"}`,
			wire.Usage{Usage: llm.Usage{Input: fakeupstream.PromptTokens}}, true},
		{"/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":5,"messages":[]}`, wire.Usage{Usage: llm.Usage{
			Input: 175, CacheRead: fakeupstream.CacheReadTokens, CacheWrite: fakeupstream.CacheWriteTokens, Output: 10}}, true},
		{"/v1/messages/count_tokens", `{"model":"claude-haiku-4-5","messages":[]}`, wire.Usage{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			e := setup(t, fakeupstream.Options{}, nil)
			res, body := e.post(t, tt.path, tt.body)
			if res.StatusCode != 200 {
				t.Fatalf("%d %s", res.StatusCode, body)
			}
			if rec := e.record(t); rec.UsageKnown != tt.known || rec.Usage != tt.usage {
				t.Fatalf("record %+v", rec)
			}
		})
	}
}

func TestTargetModelReplacesTheRequestedOne(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return gateway.Static{{Provider: "openai", BaseURL: base + "/v1", Model: "gpt-5-nano"}}
	})
	res, _ := e.post(t, "/v1/chat/completions", "{\n  \"model\": \"gpt-5\",\n  \"messages\": []\n}")
	if res.Header.Get(brand.HeaderServedModel) != "gpt-5-nano" {
		t.Fatalf("served model %q", res.Header.Get(brand.HeaderServedModel))
	}
	if up := e.fake.Requests()[0]; string(up.Body) != "{\n  \"model\": \"gpt-5-nano\",\n  \"messages\": []\n}" {
		t.Fatalf("upstream body %q", up.Body)
	}
	if rec := e.record(t); rec.RequestedModel != "gpt-5" || rec.ServedModel != "gpt-5-nano" {
		t.Fatalf("record %+v", rec)
	}
}

func TestFallbacks(t *testing.T) {
	two := func(base string) gateway.Resolver {
		return gateway.Static{
			{Provider: "openai", BaseURL: base + "/v1"},
			{Provider: "openai", BaseURL: base + "/v1", Model: "gpt-5-mini"},
		}
	}
	e := setup(t, fakeupstream.Options{}, two)
	e.fake.Fail(http.StatusServiceUnavailable)
	res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderServedModel) != "gpt-5-mini" {
		t.Fatalf("%d, served %q", res.StatusCode, res.Header.Get(brand.HeaderServedModel))
	}
	if rec := e.record(t); rec.Attempts != 2 || rec.ErrorCode != "" {
		t.Fatalf("record %+v", rec)
	}

	// When the last target fails too, the client gets the provider's answer.
	e = setup(t, fakeupstream.Options{}, two)
	e.fake.Fail(http.StatusTooManyRequests, http.StatusServiceUnavailable)
	res, body := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	if res.StatusCode != 503 || !strings.Contains(body, "fake failure") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if rec := e.record(t); rec.Attempts != 2 || rec.ErrorCode != "provider_503" || rec.UsageKnown {
		t.Fatalf("record %+v", rec)
	}

	// A client error is the client's: no fallback.
	e = setup(t, fakeupstream.Options{}, two)
	e.fake.Fail(http.StatusBadRequest)
	if res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`); res.StatusCode != 400 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if rec := e.record(t); rec.Attempts != 1 {
		t.Fatalf("record %+v", rec)
	}
}

func TestUnreachableProvider(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return gateway.Static{{Provider: "openai", BaseURL: "http://127.0.0.1:1/v1"}}
	})
	res, body := e.post(t, "/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":5,"messages":[]}`)
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(body, `"type":"error"`) ||
		res.Header.Get(brand.HeaderErrorCode) != "provider_unreachable" {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

type refuse struct{ err error }

func (r refuse) Resolve(context.Context, *gateway.Call) (*gateway.Route, error) { return nil, r.err }

func TestGatewayErrorsTakeTheClientsAPIShape(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return refuse{&gateway.Error{Status: 401, Type: gateway.TypeAuthentication, Code: "invalid_api_key",
			Message: "the API key is not valid"}}
	}, func(c *gateway.Config) { c.MaxBody = 1 << 10 })

	type openAIError struct {
		Error struct{ Message, Type, Code string } `json:"error"`
	}
	type anthropicError struct {
		Type  string                         `json:"type"`
		Error struct{ Type, Message string } `json:"error"`
	}
	res, body := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	var oe openAIError
	if res.StatusCode != 401 || json.Unmarshal([]byte(body), &oe) != nil || oe.Error.Code != "invalid_api_key" ||
		oe.Error.Type != gateway.TypeAuthentication {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	res, body = e.post(t, "/v1/messages", `{"model":"claude-haiku-4-5","messages":[]}`)
	var ae anthropicError
	if res.StatusCode != 401 || json.Unmarshal([]byte(body), &ae) != nil || ae.Type != "error" ||
		ae.Error.Type != gateway.TypeAuthentication {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if rec := e.record(t); rec.Status != 401 || rec.ErrorCode != "invalid_api_key" {
		t.Fatalf("record %+v", rec)
	}

	for _, tt := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"POST", "/v1/models/x", `{}`, 404, "unknown_endpoint"},
		{"GET", "/v1/chat/completions", ``, 405, "method_not_allowed"},
		{"POST", "/v1/chat/completions", `{"model":`, 400, "invalid_request"},
		{"POST", "/v1/messages", `{"messages":[]}`, 400, "invalid_request"},
		{"POST", "/v1/chat/completions", `{"model":"x","pad":"` + strings.Repeat("x", 2<<10) + `"}`, 413, "request_too_large"},
	} {
		req, _ := http.NewRequestWithContext(context.Background(), tt.method, e.gateway.URL+tt.path, strings.NewReader(tt.body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != tt.status || res.Header.Get(brand.HeaderErrorCode) != tt.code {
			t.Errorf("%s %s: %d %s", tt.method, tt.path, res.StatusCode, res.Header.Get(brand.HeaderErrorCode))
		}
	}
}

func TestQuietProvidersAreCut(t *testing.T) {
	e := setup(t, fakeupstream.Options{ChunkDelay: 300 * time.Millisecond}, nil,
		func(c *gateway.Config) { c.IdleTimeout = 50 * time.Millisecond })
	res, body := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","stream":true,"messages":[]}`)
	if res.StatusCode != 200 || strings.Contains(body, "[DONE]") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if rec := e.record(t); rec.ErrorCode != "provider_idle_timeout" {
		t.Fatalf("record %+v", rec)
	}
}

// routeResolver routes every call the same way, retries included.
type routeResolver struct{ route gateway.Route }

func (r routeResolver) Resolve(_ context.Context, c *gateway.Call) (*gateway.Route, error) {
	out := r.route
	out.Targets = append([]gateway.Target(nil), r.route.Targets...)
	for i := range out.Targets {
		if out.Targets[i].Model == "" {
			out.Targets[i].Model = c.Request.Model
		}
	}
	return &out, nil
}

func TestRetriesBeforeFallingBack(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return routeResolver{gateway.Route{Retries: 1, Targets: []gateway.Target{
			{ID: "a", Provider: "openai", BaseURL: base + "/v1"},
			{ID: "b", Provider: "backup", BaseURL: base + "/v1", Model: "gpt-5-nano"},
		}}}
	})
	e.fake.Fail(http.StatusBadGateway)
	res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderProvider) != "openai" {
		t.Fatalf("%d via %s: the retry should have answered", res.StatusCode, res.Header.Get(brand.HeaderProvider))
	}
	if rec := e.record(t); rec.Attempts != 2 {
		t.Fatalf("record %+v", rec)
	}
}

func TestOpenCircuitsAreSkipped(t *testing.T) {
	var failing atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		failing.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return routeResolver{gateway.Route{Targets: []gateway.Target{
			{ID: "down", Provider: "down", BaseURL: down.URL + "/v1"},
			{ID: "up", Provider: "up", BaseURL: base + "/v1"},
		}}}
	})
	for i := range 8 {
		res, _ := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","messages":[]}`)
		if res.StatusCode != 200 || res.Header.Get(brand.HeaderProvider) != "up" {
			t.Fatalf("call %d: %d via %s", i, res.StatusCode, res.Header.Get(brand.HeaderProvider))
		}
	}
	// Five failures open the circuit: the next calls go straight to the
	// fallback.
	if n := failing.Load(); n != 5 {
		t.Fatalf("the failing provider got %d calls, want 5", n)
	}
}
