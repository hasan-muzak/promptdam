package gateway_test

import (
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
	got := string(gateway.CloudBody(vertex, wire.AnthropicMessages, "/messages", []byte(body)))
	want := `{"max_tokens":9,"system":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}],"messages":[],"anthropic_version":"vertex-2023-10-16"}`
	if got != want {
		t.Fatalf("vertex: %s", got)
	}
	own := `{"model":"m","anthropic_version":"vertex-2099-01-01","messages":[]}`
	if got := string(gateway.CloudBody(vertex, wire.AnthropicMessages, "/messages", []byte(own))); got != `{"anthropic_version":"vertex-2099-01-01","messages":[]}` {
		t.Fatalf("the client's version: %s", got)
	}
	for _, tt := range []struct {
		t    gateway.Target
		f    wire.Format
		path string
	}{
		{vertex, wire.OpenAIChat, "/chat/completions"}, {vertex, wire.AnthropicCountTokens, "/messages/count_tokens"},
		{gateway.Target{Kind: "bedrock"}, wire.AnthropicMessages, "/messages"},
	} {
		if got := string(gateway.CloudBody(tt.t, tt.f, tt.path, []byte(body))); got != body {
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
