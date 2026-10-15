package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// Each cloud's API for a format: Claude through Bedrock's, Foundry's and
// Vertex's Anthropic APIs (Vertex's with the model in the URL), the others
// through their OpenAI-compatible ones.
func TestUpstreamURL(t *testing.T) {
	bedrock := gateway.Target{Kind: "bedrock", BaseURL: "https://bedrock-mantle.eu-west-1.api.aws", Model: "anthropic.claude-haiku-5-5"}
	vertex := gateway.Target{Kind: "vertex", BaseURL: "https://aiplatform.googleapis.com/v1/projects/acme-prod/locations/global",
		Model: "claude-haiku-5-5"}
	azure := gateway.Target{Kind: "azure", BaseURL: "https://acme.services.ai.azure.com", Model: "gpt-6"}
	openai := gateway.Target{Kind: "openai", BaseURL: "https://api.openai.com/v1/"}
	for _, tt := range []struct {
		t      gateway.Target
		f      wire.Format
		path   string
		stream bool
		want   string
	}{
		{bedrock, wire.AnthropicMessages, "/messages", true, "https://bedrock-mantle.eu-west-1.api.aws/anthropic/v1/messages"},
		{bedrock, wire.OpenAIChat, "/chat/completions", false,
			"https://bedrock-runtime.eu-west-1.amazonaws.com/openai/v1/chat/completions"},
		{vertex, wire.AnthropicMessages, "/messages", true, vertex.BaseURL + "/publishers/anthropic/models/claude-haiku-5-5:streamRawPredict"},
		{vertex, wire.AnthropicMessages, "/messages", false, vertex.BaseURL + "/publishers/anthropic/models/claude-haiku-5-5:rawPredict"},
		{vertex, wire.AnthropicCountTokens, "/messages/count_tokens", false,
			vertex.BaseURL + "/publishers/anthropic/models/count-tokens:rawPredict"},
		{vertex, wire.OpenAIChat, "/chat/completions", true, vertex.BaseURL + "/endpoints/openapi/chat/completions"},
		{gateway.Target{Kind: "vertex", BaseURL: vertex.BaseURL, Model: "meta/llama-3.3-70b-instruct-maas"}, wire.OpenAIChat,
			"/chat/completions", true, vertex.BaseURL + "/endpoints/openapi/chat/completions"},
		{gateway.Target{Kind: "vertex", BaseURL: vertex.BaseURL, Model: "mistral-medium-3"}, wire.OpenAIChat, "/chat/completions",
			true, vertex.BaseURL + "/publishers/mistralai/models/mistral-medium-3:streamRawPredict"},
		{gateway.Target{Kind: "vertex", BaseURL: vertex.BaseURL, Model: "codestral-2"}, wire.OpenAIChat, "/chat/completions",
			false, vertex.BaseURL + "/publishers/mistralai/models/codestral-2:rawPredict"},
		{azure, wire.OpenAIChat, "/chat/completions", true, "https://acme.services.ai.azure.com/openai/v1/chat/completions"},
		{azure, wire.OpenAIResponses, "/responses", false, "https://acme.services.ai.azure.com/openai/v1/responses"},
		{azure, wire.AnthropicMessages, "/messages", false, "https://acme.services.ai.azure.com/anthropic/v1/messages"},
		{openai, wire.OpenAIChat, "/chat/completions", false, "https://api.openai.com/v1/chat/completions"},
	} {
		if got := gateway.UpstreamURL(tt.t, tt.f, tt.path, tt.stream); got != tt.want {
			t.Errorf("%s %s: %s, want %s", tt.t.Kind, tt.path, got, tt.want)
		}
	}
	// A model id with a slash or a colon stays one path segment.
	odd := vertex
	odd.Model = "claude-x@2026:01/a"
	if got := gateway.UpstreamURL(odd, wire.AnthropicMessages, "/messages", false); !strings.HasSuffix(got, "/models/claude-x@2026:01%2Fa:rawPredict") {
		t.Errorf("escaped: %s", got)
	}
}

// Claude on Vertex takes the model in the URL and its API version in the
// body; every other byte stays. Other clouds, and counting tokens, keep the
// body.
func TestCloudBody(t *testing.T) {
	vertex := gateway.Target{Kind: "vertex"}
	body := `{"model":"claude-haiku-5-5","max_tokens":9,"system":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}],"messages":[]}`
	cloudBody := func(t gateway.Target, f wire.Format, path string, body []byte) []byte {
		out, _, _ := gateway.CloudBody(t, f, path, body)
		return out
	}
	got := string(cloudBody(vertex, wire.AnthropicMessages, "/messages", []byte(body)))
	want := `{"max_tokens":9,"system":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}],"messages":[],"anthropic_version":"vertex-2023-10-16"}`
	if got != want {
		t.Fatalf("vertex: %s", got)
	}
	own := `{"model":"m","anthropic_version":"vertex-2099-01-01","messages":[]}`
	if got := string(cloudBody(vertex, wire.AnthropicMessages, "/messages", []byte(own))); got != `{"anthropic_version":"vertex-2099-01-01","messages":[]}` {
		t.Fatalf("the client's version: %s", got)
	}
	for _, tt := range []struct {
		t    gateway.Target
		f    wire.Format
		path string
	}{
		{vertex, wire.OpenAIChat, "/chat/completions"}, {vertex, wire.AnthropicCountTokens, "/messages/count_tokens"},
		{gateway.Target{Kind: "bedrock", Model: "anthropic.claude-haiku-5-5"}, wire.AnthropicMessages, "/messages"},
	} {
		if got := string(cloudBody(tt.t, tt.f, tt.path, []byte(body))); got != body {
			t.Errorf("%s %s changed: %s", tt.t.Kind, tt.path, got)
		}
	}
}

// recorder is an authorizer that signs with a header and keeps the bytes it
// signed.
type recorder struct {
	mu     sync.Mutex
	signed [][]byte
	fail   error
}

func (r *recorder) Authorize(_ context.Context, req *http.Request, body []byte) error {
	if r.fail != nil {
		return r.fail
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signed = append(r.signed, append([]byte(nil), body...))
	req.Header.Set("Authorization", "Signed")
	return nil
}

// Calls to cloud accounts go to each cloud's API for the model's family,
// signed over exactly the bytes sent; a call the credentials can't sign is
// the provider's failure, never sent unsigned.
func TestCloudCalls(t *testing.T) {
	type call struct {
		kind, base, model string
		format            wire.Format
		path, body        string
		wantURL           string
		header            http.Header
	}
	messages := func(model string) string {
		return `{"model":"` + model + `","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	}
	chat := func(model string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	}
	for _, tt := range []call{
		{"vertex", "/v1/projects/acme-prod/locations/global", "claude-haiku-5-5", wire.AnthropicMessages, "/v1/messages",
			messages("claude-haiku-5-5"), "/v1/projects/acme-prod/locations/global/publishers/anthropic/models/claude-haiku-5-5:streamRawPredict", nil},
		{"vertex", "/v1/projects/acme-prod/locations/global", "google/gemini-3.8-flash", wire.OpenAIChat, "/v1/chat/completions",
			chat("google/gemini-3.8-flash"), "/v1/projects/acme-prod/locations/global/endpoints/openapi/chat/completions", nil},
		{"vertex", "/v1/projects/acme-prod/locations/europe-west4", "mistral-medium-3", wire.OpenAIChat, "/v1/chat/completions",
			chat("mistral-medium-3"), "/v1/projects/acme-prod/locations/europe-west4/publishers/mistralai/models/mistral-medium-3:rawPredict", nil},
		{"bedrock", "", "anthropic.claude-haiku-5-5", wire.AnthropicMessages, "/v1/messages", messages("anthropic.claude-haiku-5-5"),
			"/anthropic/v1/messages", nil},
		{"azure", "", "gpt-6", wire.OpenAIChat, "/v1/chat/completions", chat("gpt-6"), "/openai/v1/chat/completions",
			http.Header{"Api-Key": {"azure-key"}}},
		{"azure", "", "claude-haiku-5-5", wire.AnthropicMessages, "/v1/messages", messages("claude-haiku-5-5"), "/anthropic/v1/messages",
			http.Header{"Api-Key": {"azure-key"}}},
	} {
		t.Run(tt.kind+" "+tt.model, func(t *testing.T) {
			auth := &recorder{}
			e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
				target := gateway.Target{Provider: tt.kind, Kind: tt.kind, BaseURL: base + tt.base, Model: tt.model, Header: tt.header}
				if tt.header == nil {
					target.Auth = auth
				}
				return routeResolver{gateway.Route{Targets: []gateway.Target{target}}}
			})
			res, out := e.post(t, tt.path, tt.body)
			if res.StatusCode != 200 {
				t.Fatalf("%d %s", res.StatusCode, out)
			}
			reqs := e.fake.Requests()
			got := reqs[len(reqs)-1]
			if got.URLPath != tt.wantURL {
				t.Fatalf("called %s, want %s", got.URLPath, tt.wantURL)
			}
			if tt.header != nil {
				if got.Header.Get("Api-Key") != "azure-key" {
					t.Fatalf("headers %v", got.Header)
				}
				return
			}
			if got.Header.Get("Authorization") != "Signed" || len(auth.signed) != 1 || string(auth.signed[0]) != string(got.Body) {
				t.Fatalf("signed %q, sent %q", auth.signed, got.Body)
			}
			if tt.kind == "vertex" && tt.format == wire.AnthropicMessages {
				var b map[string]any
				_ = json.Unmarshal(got.Body, &b)
				if _, has := b["model"]; has || b["anthropic_version"] != "vertex-2023-10-16" {
					t.Fatalf("vertex body %s", got.Body)
				}
			}
		})
	}

	// Credentials that can't sign: the call fails, never sent unsigned.
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return routeResolver{gateway.Route{Targets: []gateway.Target{{Provider: "vertex", Kind: "vertex", BaseURL: base,
			Auth: &recorder{fail: errors.New("Google refused the service-account key")}}}}}
	})
	res, out := e.post(t, "/v1/messages", messages("claude-haiku-5-5"))
	if res.StatusCode != http.StatusBadGateway || e.record(t).ErrorCode != "provider_auth_failed" || len(e.fake.Requests()) != 0 {
		t.Fatalf("unsigned: %d %s, %d sent", res.StatusCode, out, len(e.fake.Requests()))
	}
}

// Claude on a cloud caches its conversation with breakpoints on the
// messages (Anthropic's top-level one is first-party only), within
// Anthropic's four; Azure OpenAI takes the conversation's prompt_cache_key.
func TestCloudCaching(t *testing.T) {
	system, _ := json.Marshal(strings.Repeat("Follow the house style and cite the clauses. ", 150))
	conv := `{"model":"claude-haiku-5-5","max_tokens":9,"system":` + string(system) + `,"tools":[{"name":"ls","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return routeResolver{gateway.Route{PromptCache: true, Targets: []gateway.Target{{Provider: "bedrock", Kind: "bedrock",
			BaseURL: base, Model: "anthropic.claude-haiku-5-5", MinCache: 512, Auth: &recorder{}}}}}
	})
	if res, out := e.post(t, "/v1/messages", conv); res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, out)
	}
	reqs := e.fake.Requests()
	sent := string(reqs[len(reqs)-1].Body)
	var top map[string]json.RawMessage
	_ = json.Unmarshal([]byte(sent), &top)
	if n := strings.Count(sent, `"cache_control"`); n != 4 || top["cache_control"] != nil {
		t.Fatalf("%d breakpoints, top-level %s: %s", n, top["cache_control"], sent)
	}
	if !e.record(t).PromptCached {
		t.Fatal("not recorded as cached")
	}

	chat := `{"model":"gpt-6","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	azure := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return routeResolver{gateway.Route{PromptCache: true, PromptKey: "pd-conv", Targets: []gateway.Target{{Provider: "azure",
			Kind: "azure", BaseURL: base, Model: "gpt-6", Header: http.Header{"Api-Key": {"k"}}}}}}
	})
	_, _ = azure.post(t, "/v1/chat/completions", chat)
	reqs = azure.fake.Requests()
	if body := string(reqs[len(reqs)-1].Body); !strings.Contains(body, `"prompt_cache_key":"pd-conv"`) || strings.Contains(body, "cache_control") {
		t.Fatalf("azure: %s", body)
	}
}

// Bedrock's models other than the Claude its Anthropic API serves go
// through Converse on bedrock-runtime, the id escaped as AWS's SDKs send
// it; older Claude named by its profile id too.
func TestConverseURL(t *testing.T) {
	base := "https://bedrock-mantle.eu-west-1.api.aws"
	for model, want := range map[string]string{
		"us.amazon.nova-lite-v1:0":                     "https://bedrock-runtime.eu-west-1.amazonaws.com/model/us.amazon.nova-lite-v1%3A0/converse-stream",
		"meta.llama3-3-70b-instruct-v1:0":              "https://bedrock-runtime.eu-west-1.amazonaws.com/model/meta.llama3-3-70b-instruct-v1%3A0/converse-stream",
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0": "https://bedrock-runtime.eu-west-1.amazonaws.com/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse-stream",
		"anthropic.claude-haiku-5-5":                   base + "/anthropic/v1/messages",
	} {
		tg := gateway.Target{Kind: "bedrock", BaseURL: base, Model: model}
		if got := gateway.UpstreamURL(tg, wire.AnthropicMessages, "/messages", true); got != want {
			t.Errorf("%s: %s", model, got)
		}
	}
	tg := gateway.Target{Kind: "bedrock", BaseURL: base, Model: "us.amazon.nova-lite-v1:0"}
	if got := gateway.UpstreamURL(tg, wire.AnthropicMessages, "/messages", false); !strings.HasSuffix(got, "/converse") {
		t.Errorf("not streamed: %s", got)
	}
}

// Calls to Bedrock's other models are spoken in Converse and answered in
// the client's own API: Messages and Chat clients, whole and streamed,
// signed over the Converse bytes sent; AWS's throttling, before the answer
// or in it, reads as the client's API says it; what Converse can't express
// is refused.
func TestConverseCalls(t *testing.T) {
	type world struct {
		e    *env
		auth *recorder
	}
	bedrock := func(model string, format wire.Format, cache string) world {
		auth := &recorder{}
		e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
			return routeResolver{gateway.Route{PromptCache: cache != "", Targets: []gateway.Target{{Provider: "bedrock", Kind: "bedrock",
				BaseURL: base, Model: model, Format: format, MinCache: 512, Auth: auth, Cache: gateway.CachePlan{Strategy: cache}}}}}
		})
		return world{e, auth}
	}
	messages := func(model, extra string) string {
		return `{"model":"` + model + `","max_tokens":50` + extra + `,"messages":[{"role":"user","content":"hi"}]}`
	}

	w := bedrock("us.amazon.nova-lite-v1:0", 0, "")
	res, out := w.e.post(t, "/v1/messages", messages("us.amazon.nova-lite-v1:0", ""))
	if res.StatusCode != 200 || !strings.Contains(out, `"text":"tok0 tok1`) || !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatalf("messages: %d %s", res.StatusCode, out)
	}
	got := w.e.fake.Requests()[0]
	if got.URLPath != "/model/us.amazon.nova-lite-v1:0/converse" || bytes.Contains(got.Body, []byte(`"model"`)) ||
		string(w.auth.signed[0]) != string(got.Body) || got.Header.Get("Anthropic-Version") != "" {
		t.Fatalf("sent %s %s %v", got.URLPath, got.Body, got.Header)
	}
	if rec := w.e.record(t); !rec.UsageKnown || rec.Usage.Input != fakeupstream.PromptTokens || rec.Usage.Output != 10 {
		t.Fatalf("record %+v", rec.Usage)
	}

	res, out = w.e.post(t, "/v1/messages", messages("us.amazon.nova-lite-v1:0", `,"stream":true`))
	if res.StatusCode != 200 || !strings.Contains(out, "event: message_start") || !strings.Contains(out, `"text_delta"`) ||
		!strings.Contains(out, "event: message_stop") || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("messages streamed: %d %s", res.StatusCode, out)
	}
	if rec := w.e.record(t); rec.Usage.Output != 10 || rec.Usage.Input != fakeupstream.PromptTokens {
		t.Fatalf("streamed record %+v", rec.Usage)
	}

	// A Chat client reaches Llama: Chat → Messages → Converse, and back.
	llama := bedrock("meta.llama3-3-70b-instruct-v1:0", wire.AnthropicMessages, "")
	chat := `{"model":"meta.llama3-3-70b-instruct-v1:0","messages":[{"role":"user","content":"hi"}]}`
	res, out = llama.e.post(t, "/v1/chat/completions", chat)
	if res.StatusCode != 200 || !strings.Contains(out, `"object":"chat.completion"`) || !strings.Contains(out, "tok0 tok1") {
		t.Fatalf("chat: %d %s", res.StatusCode, out)
	}
	res, out = llama.e.post(t, "/v1/chat/completions", strings.Replace(chat, `"messages"`, `"stream":true,"messages"`, 1))
	if res.StatusCode != 200 || !strings.Contains(out, `"chat.completion.chunk"`) || !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("chat streamed: %d %s", res.StatusCode, out)
	}

	// Throttled before the answer: Anthropic's rate-limit error.
	w.e.fake.Fail(429, 429, 429)
	res, out = w.e.post(t, "/v1/messages", messages("us.amazon.nova-lite-v1:0", ""))
	if res.StatusCode != 429 || !strings.Contains(out, "rate_limit_error") || !strings.Contains(out, "ThrottlingException") {
		t.Fatalf("throttled: %d %s", res.StatusCode, out)
	}

	// Throttled mid-way: an error event ends the stream.
	mid := bedrock("us.amazon.nova-throttle-mid-stream-v1:0", 0, "")
	res, out = mid.e.post(t, "/v1/messages", messages("us.amazon.nova-throttle-mid-stream-v1:0", `,"stream":true`))
	if !strings.Contains(out, "event: error") || !strings.Contains(out, "overloaded_error") {
		t.Fatalf("throttled mid-way: %d %s", res.StatusCode, out)
	}

	// An image by URL: Converse takes images themselves.
	res, out = w.e.post(t, "/v1/messages", `{"model":"us.amazon.nova-lite-v1:0","max_tokens":9,"messages":[{"role":"user",`+
		`"content":[{"type":"image","source":{"type":"url","url":"https://x/y.png"}}]}]}`)
	if res.StatusCode != 400 || w.e.record(t).ErrorCode != "untranslatable_request" {
		t.Fatalf("an image by URL: %d %s", res.StatusCode, out)
	}
}

// Nova caches what a request marks: breakpoints become checkpoints and its
// cache reads are metered; Llama doesn't, and is sent none.
func TestConverseCaching(t *testing.T) {
	system, _ := json.Marshal(strings.Repeat("Follow the house style and cite the clauses. ", 150))
	conv := func(model string) string {
		return `{"model":"` + model + `","max_tokens":9,"system":` + string(system) + `,"messages":[{"role":"user","content":"a"},` +
			`{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	}
	for _, tt := range []struct {
		model, strategy string
		points          int
	}{
		{"us.amazon.nova-lite-v1:0", "markers", 3},
		{"meta.llama3-3-70b-instruct-v1:0", "auto", 0},
	} {
		e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
			return routeResolver{gateway.Route{PromptCache: true, Targets: []gateway.Target{{Provider: "bedrock", Kind: "bedrock",
				BaseURL: base, Model: tt.model, MinCache: 512, Auth: &recorder{}, Cache: gateway.CachePlan{Strategy: tt.strategy}}}}}
		})
		if res, out := e.post(t, "/v1/messages", conv(tt.model)); res.StatusCode != 200 {
			t.Fatalf("%s: %d %s", tt.model, res.StatusCode, out)
		}
		sent := e.fake.Requests()[0].Body
		rec := e.record(t)
		if n := bytes.Count(sent, []byte(`"cachePoint"`)); n != tt.points || bytes.Contains(sent, []byte("cache_control")) {
			t.Fatalf("%s: %d checkpoints: %s", tt.model, n, sent)
		}
		if tt.points > 0 && (rec.Usage.CacheRead != fakeupstream.CacheReadTokens || !rec.PromptCached) {
			t.Fatalf("%s: usage %+v", tt.model, rec.Usage)
		}
	}
}
