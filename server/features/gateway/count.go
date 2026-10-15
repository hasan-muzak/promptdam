package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// Counting tokens through Gemini's door: Google counts for Gemini (on its
// own API or Vertex, which the OpenAI-compatible endpoint the gateway
// calls Gemini through has no count for), Anthropic for Claude (the call
// translated, as for an answer); other models can't count.

// googleCounts reports a target Google counts for natively.
func googleCounts(t Target) bool {
	return t.Kind == "gemini" || t.Kind == "vertex" && strings.Contains(strings.ToLower(t.Model), "gemini")
}

// googleCountURL is a Gemini model's countTokens: under Google's API, the
// gemini kind's base URL less its /openai; on Vertex, the model's
// publisher path.
func googleCountURL(t Target) string {
	base := strings.TrimRight(t.BaseURL, "/")
	if t.Kind == "vertex" {
		return base + "/publishers/google/models/" + url.PathEscape(strings.TrimPrefix(t.Model, "google/")) + ":countTokens"
	}
	return strings.TrimSuffix(base, "/openai") + "/models/" + url.PathEscape(t.Model) + ":countTokens"
}

// countFields are the fields of a Messages request its count takes.
var countFields = []string{"model", "messages", "system", "tools", "tool_choice", "thinking", "mcp_servers"}

// countBody is a Messages request as its count takes it.
func countBody(body []byte) []byte {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return body
	}
	out := make(map[string]json.RawMessage, len(countFields))
	for _, k := range countFields {
		if v, ok := top[k]; ok {
			out[k] = v
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return body
	}
	return b
}

// attemptCount counts a Gemini countTokens call's tokens with t (body is
// the call as Chat Completions); like attempt, it says what the call does
// next.
func (c *call) attemptCount(t Target, body []byte, lastTarget, lastTry bool) (next outcome, failed bool) {
	canAgain, canOnward := !lastTarget || !lastTry, !lastTarget
	refuse := func(msg string) (outcome, bool) {
		if canOnward {
			return onward, false
		}
		c.fail(&Error{Status: http.StatusBadRequest, Type: TypeInvalidRequest, Code: "untranslatable_request", Message: msg})
		return answered, false
	}
	var target string
	var send []byte
	format := wire.AnthropicCountTokens
	switch {
	case googleCounts(t):
		if c.rewrote {
			return refuse("the firewall changed the request, and Google would count it as you sent it")
		}
		send, format, target = c.door.native, wire.OpenAIChat, googleCountURL(t)
	case t.formatOf(wire.OpenAIChat) == wire.AnthropicMessages:
		out, _, err := translate.Request(wire.OpenAIChat, wire.AnthropicMessages, body, translate.Options{Model: t.Model, Kind: t.Kind})
		if err != nil {
			return refuse("the request can't be counted for " + t.Model)
		}
		if send, _, err = CloudBody(t, format, "/messages/count_tokens", countBody(out)); err != nil {
			return refuse("counting tokens isn't available for " + t.Model)
		}
		target = UpstreamURL(t, format, "/messages/count_tokens", false)
	default:
		return refuse("counting tokens isn't available for " + t.Model)
	}
	ctx, cancel := context.WithCancel(c.r.Context())
	defer cancel()
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(send))
	if err != nil {
		c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_misconfigured", Message: "the provider's base URL is invalid"})
		return answered, false
	}
	upstreamHeader(up.Header, c.r.Header, format, t.Header)
	if t.Kind == "gemini" {
		// Google's own API takes the key as x-goog-api-key, not a bearer token.
		if a := up.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
			up.Header.Del("Authorization")
			up.Header.Set("X-Goog-Api-Key", a[7:])
		}
	}
	CloudHeader(t, format, up.Header, false)
	if err := Sign(ctx, t, up, send); err != nil {
		c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_auth_failed",
			Message: "the provider's credentials couldn't sign the call: " + err.Error()})
		return answered, false
	}
	sent := time.Now()
	res, err := c.h.cfg.Transport.RoundTrip(up)
	c.rec.Upstream += time.Since(sent)
	if err != nil {
		switch {
		case c.r.Context().Err() != nil:
			c.rec.Status, c.rec.ErrorCode = statusClientClosed, "client_closed"
			return answered, false
		case canAgain:
			return again, true
		}
		c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_unreachable", Message: "the provider could not be reached"})
		return answered, true
	}
	defer func() { _ = res.Body.Close() }()
	switch {
	case retryable(res.StatusCode) && (canAgain || canOnward):
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		if canAgain {
			return again, true
		}
		return onward, true
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_auth_failed",
			Message: "the provider refused the project's key or account for it (" + strconv.Itoa(res.StatusCode) +
				"): check the provider's key in Promptdam"})
		return answered, false
	case res.StatusCode >= 400:
		raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		maskError(res, raw)
	}
	c.relay(res, t, cancel, false, c.ep.format, nil)
	return answered, retryable(res.StatusCode)
}

// countOf is a count answer's input tokens: Anthropic's input_tokens, or
// Google's totalTokens.
func countOf(body []byte) (int64, bool, error) {
	var in struct {
		InputTokens *int64 `json:"input_tokens"`
		TotalTokens *int64 `json:"totalTokens"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return 0, false, err
	}
	switch {
	case in.InputTokens != nil:
		return *in.InputTokens, true, nil
	case in.TotalTokens != nil:
		return *in.TotalTokens, false, nil
	}
	return 0, false, errors.New("the count answer names no tokens")
}
