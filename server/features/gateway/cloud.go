package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// cloudKind reports a cloud account's kind (providers.IsCloud).
func cloudKind(kind string) bool { return kind == "bedrock" || kind == "vertex" || kind == "azure" }

// vertexVersion is the API version Claude on Vertex AI takes in the body.
const vertexVersion = `"vertex-2023-10-16"`

// UpstreamURL is where a call in format f to target t goes: the provider's
// base URL and the endpoint's path (path: /chat/completions, /messages, …),
// or, on a cloud account, the cloud's API for the format:
//
//   - bedrock: Claude through Bedrock's Anthropic API (bedrock-mantle),
//     others through its OpenAI-compatible one (bedrock-runtime);
//   - vertex: Claude through rawPredict, the model in the URL, others
//     through the OpenAI-compatible endpoint;
//   - azure: Claude through Foundry's Anthropic API, others through Azure
//     OpenAI's v1 API.
func UpstreamURL(t Target, f wire.Format, path string, stream bool) string {
	base := strings.TrimRight(t.BaseURL, "/")
	switch t.Kind {
	case "bedrock":
		if f.Anthropic() {
			return base + "/anthropic/v1" + path
		}
		return bedrockRuntime(base) + "/openai/v1" + path
	case "vertex":
		if !f.Anthropic() {
			return base + "/endpoints/openapi" + path
		}
		if path == "/messages/count_tokens" {
			return base + "/publishers/anthropic/models/count-tokens:rawPredict"
		}
		method := ":rawPredict"
		if stream {
			method = ":streamRawPredict"
		}
		return base + "/publishers/anthropic/models/" + url.PathEscape(t.Model) + method
	case "azure":
		if f.Anthropic() {
			return base + "/anthropic/v1" + path
		}
		return base + "/openai/v1" + path
	}
	return base + path
}

// bedrockRuntime is the OpenAI-compatible API's host of a Bedrock account
// (its base URL is the Anthropic API's: bedrock-mantle.{region}.api.aws).
func bedrockRuntime(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	if region, ok := strings.CutPrefix(u.Hostname(), "bedrock-mantle."); ok {
		if region, ok = strings.CutSuffix(region, ".api.aws"); ok {
			u.Host = "bedrock-runtime." + region + ".amazonaws.com"
			u.Path = ""
			return u.String()
		}
	}
	return base // a fake cloud in tests: one host for both
}

// CloudBody fits a request's body to its cloud's API: Claude on Vertex
// takes the model in the URL (UpstreamURL), not in the body, and its API
// version in the body. Every other byte stays, so prompt caches still
// match.
func CloudBody(t Target, f wire.Format, path string, body []byte) []byte {
	if t.Kind != "vertex" || !f.Anthropic() || path == "/messages/count_tokens" {
		return body
	}
	out, err := wire.DeleteField(body, "model")
	if err != nil {
		return body
	}
	if _, set, err := wire.Field(out, "anthropic_version"); err == nil && !set {
		if versioned, err := wire.SetField(out, "anthropic_version", []byte(vertexVersion)); err == nil {
			out = versioned
		}
	}
	return out
}

// Sign signs a request to t, its headers set and body the bytes sent, for
// a cloud that signs each request (Target.Auth); others' keys are in their
// headers already.
func Sign(ctx context.Context, t Target, req *http.Request, body []byte) error {
	if t.Auth == nil {
		return nil
	}
	return t.Auth.Authorize(ctx, req, body)
}
