package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/converse"
	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// cloudKind reports a cloud account's kind (providers.IsCloud).
func cloudKind(kind string) bool { return kind == "bedrock" || kind == "vertex" || kind == "azure" }

// vertexVersion is the API version Claude on Vertex AI takes in the body.
const vertexVersion = `"vertex-2023-10-16"`

// UpstreamURL is where a call in format f to target t goes: the provider's
// base URL and the endpoint's path (path: /chat/completions, /messages, …),
// or, on a cloud account, the cloud's API for the format:
//
//   - bedrock: Claude in its current form through Bedrock's Anthropic API
//     (bedrock-mantle), every other model through Converse
//     (bedrock-runtime);
//   - vertex: Claude through rawPredict, the model in the URL, others
//     through the OpenAI-compatible endpoint;
//   - azure: Claude through Foundry's Anthropic API, others through Azure
//     OpenAI's v1 API.
func UpstreamURL(t Target, f wire.Format, path string, stream bool) string {
	base := strings.TrimRight(t.BaseURL, "/")
	switch t.Kind {
	case "bedrock":
		switch {
		case Converse(t, f):
			method := "/converse"
			if stream {
				method = "/converse-stream"
			}
			// The id escaped as AWS's SDKs send it (":" as %3A): SigV4
			// signs the path as sent.
			return bedrockRuntime(base) + "/model/" + awsEscape(t.Model) + method
		case f.Anthropic():
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

// Converse reports a call to Bedrock in its Converse dialect: a Messages
// call to any model but the Claude its Anthropic API serves
// (converse.For).
func Converse(t Target, f wire.Format) bool {
	return t.Kind == "bedrock" && f.Anthropic() && converse.For(t.Model)
}

// awsEscape escapes a model id as AWS's SDKs do: everything but RFC 3986's
// unreserved characters.
func awsEscape(id string) string {
	var b strings.Builder
	for i := 0; i < len(id); i++ {
		c := id[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// CloudBody fits a request's body to its cloud's API: Claude on Vertex
// takes the model in the URL (UpstreamURL), not in the body, and its API
// version in the body, every other byte kept so prompt caches still match;
// Bedrock's Converse takes its own request (converse.Request), what it
// can't carry listed in dropped. A request Converse can't express at all
// is refused (*translate.Error); counting tokens has no Converse form.
func CloudBody(t Target, f wire.Format, path string, body []byte) (out []byte, dropped []string, err error) {
	if Converse(t, f) {
		if path == "/messages/count_tokens" {
			return nil, nil, &translate.Error{Message: "counting tokens (Bedrock's Converse has no count)"}
		}
		return converse.Request(body, t.Model)
	}
	if t.Kind != "vertex" || !f.Anthropic() || path == "/messages/count_tokens" {
		return body, nil, nil
	}
	out, err = wire.DeleteField(body, "model")
	if err != nil {
		return body, nil, nil
	}
	if _, set, err := wire.Field(out, "anthropic_version"); err == nil && !set {
		if versioned, err := wire.SetField(out, "anthropic_version", []byte(vertexVersion)); err == nil {
			out = versioned
		}
	}
	return out, nil, nil
}

// CloudHeader fits a request's headers to its cloud's API: Converse takes
// none of Anthropic's, and answers a stream in AWS's event stream.
func CloudHeader(t Target, f wire.Format, h http.Header, stream bool) {
	if !Converse(t, f) {
		return
	}
	h.Del("Anthropic-Version")
	h.Del("Anthropic-Beta")
	if stream {
		h.Set("Accept", "application/vnd.amazon.eventstream")
	} else {
		h.Set("Accept", "application/json")
	}
}

// maxCloudAnswer bounds a whole answer read to convert it.
const maxCloudAnswer = 64 << 20

// CloudResponse turns a cloud's answer into its format's, in place, before
// anything reads it: Converse's JSON, event stream and errors into
// Messages' (converse.Response, Stream, Error). Others stay as they came.
// Closing res.Body still closes the answer.
func CloudResponse(t Target, f wire.Format, res *http.Response) {
	if !Converse(t, f) {
		return
	}
	id := converse.MessageID(res.Header)
	set := func(status int, body []byte) {
		res.StatusCode = status
		res.Body = io.NopCloser(bytes.NewReader(body))
		res.ContentLength = int64(len(body))
		res.Header.Set("Content-Type", "application/json")
		res.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	switch {
	case res.StatusCode < 300 && strings.HasPrefix(res.Header.Get("Content-Type"), "application/vnd.amazon.eventstream"):
		res.Body = converse.Stream(res.Body, t.Model, id)
		res.ContentLength = -1
		res.Header.Del("Content-Length")
		res.Header.Set("Content-Type", "text/event-stream")
	case res.StatusCode < 300:
		raw, err := io.ReadAll(io.LimitReader(res.Body, maxCloudAnswer))
		_ = res.Body.Close()
		out, cerr := converse.Response(raw, t.Model, id)
		if err != nil || cerr != nil {
			status, body := converse.Error(http.StatusBadGateway, http.Header{}, []byte(`{"message":"Bedrock's answer doesn't read"}`))
			set(status, body)
			return
		}
		set(res.StatusCode, out)
	default:
		raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		_ = res.Body.Close()
		status, body := converse.Error(res.StatusCode, res.Header, raw)
		set(status, body)
	}
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
