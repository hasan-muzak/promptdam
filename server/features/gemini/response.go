package gemini

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

// The Chat Completions answer, as far as Gemini's reads it.
type chatCompletion struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage"`
}

type chatChoice struct {
	Index        int        `json:"index"`
	Message      chatAnswer `json:"message"`
	FinishReason *string    `json:"finish_reason"`
}

type chatAnswer struct {
	Content   json.RawMessage `json:"content"`
	Refusal   *string         `json:"refusal"`
	ToolCalls []chatToolCall  `json:"tool_calls"`
}

type chatUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"` // DeepSeek's
}

// The GenerateContentResponse.
type gResponse struct {
	Candidates    []gCandidate `json:"candidates"`
	UsageMetadata *gUsage      `json:"usageMetadata,omitempty"`
	ModelVersion  string       `json:"modelVersion,omitempty"`
	ResponseID    string       `json:"responseId,omitempty"`
}

type gCandidate struct {
	Content      gContent `json:"content"`
	FinishReason string   `json:"finishReason,omitempty"`
	Index        int      `json:"index"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gPart struct {
	Text             *string `json:"text,omitempty"`
	FunctionCall     *gCall  `json:"functionCall,omitempty"`
	ThoughtSignature string  `json:"thoughtSignature,omitempty"`
}

type gCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// gUsage is Gemini's usage: the prompt's count includes what was read from
// the cache, the candidates' leaves out the thinking, counted on its own.
type gUsage struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitempty"`
}

// Response turns a Chat Completions answer into Gemini's.
func Response(body []byte) ([]byte, error) {
	var in chatCompletion
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	out := gResponse{ModelVersion: in.Model, ResponseID: in.ID, UsageMetadata: usageOf(in.Usage), Candidates: []gCandidate{}}
	for _, c := range in.Choices {
		var parts []gPart
		if s := contentText(c.Message.Content); s != "" {
			parts = append(parts, textPart(s))
		}
		if c.Message.Refusal != nil && *c.Message.Refusal != "" {
			parts = append(parts, textPart(*c.Message.Refusal))
		}
		for _, tc := range c.Message.ToolCalls {
			parts = append(parts, callPart(tc.ID, tc.Function.Name, tc.Function.Arguments, tc.Extra))
		}
		reason := "STOP"
		if c.FinishReason != nil {
			reason = finishReason(*c.FinishReason)
		}
		out.Candidates = append(out.Candidates, candidate(c.Index, parts, reason))
	}
	return marshal(out)
}

// candidate is an answer; one without parts has an empty text, as
// Gemini's last streamed chunk does.
func candidate(index int, parts []gPart, reason string) gCandidate {
	if len(parts) == 0 {
		parts = []gPart{textPart("")}
	}
	return gCandidate{Content: gContent{Role: "model", Parts: parts}, FinishReason: reason, Index: index}
}

func textPart(s string) gPart { return gPart{Text: &s} }

// callPart is a function call; arguments that aren't a JSON object (a
// model's broken output) are kept as a string, under "arguments".
func callPart(id, name, args string, extra *chatExtras) gPart {
	raw := json.RawMessage(strings.TrimSpace(args))
	switch {
	case len(raw) == 0:
		raw = json.RawMessage(`{}`)
	case raw[0] != '{' || !json.Valid(raw):
		raw, _ = marshal(map[string]string{"arguments": args})
	}
	p := gPart{FunctionCall: &gCall{ID: id, Name: name, Args: raw}}
	if extra != nil {
		p.ThoughtSignature = extra.Google.ThoughtSignature
	}
	return p
}

// contentText is an answer's text: a string, or text parts.
func contentText(raw json.RawMessage) string {
	if null(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// finishReason is Gemini's finishReason for a Chat finish_reason.
func finishReason(r string) string {
	switch r {
	case "stop", "tool_calls", "function_call", "":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	}
	return "OTHER"
}

// usageOf is Chat's usage the Gemini way.
func usageOf(u *chatUsage) *gUsage {
	if u == nil {
		return nil
	}
	var reasoning, cached int64
	if u.CompletionTokensDetails != nil {
		reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
	}
	if cached == 0 {
		cached = u.PromptCacheHitTokens
	}
	return &gUsage{PromptTokenCount: u.PromptTokens, CandidatesTokenCount: max(u.CompletionTokens-reasoning, 0),
		ThoughtsTokenCount: reasoning, CachedContentTokenCount: cached, TotalTokenCount: u.PromptTokens + u.CompletionTokens}
}

// statuses are Google's names for HTTP statuses.
var statuses = map[int]string{
	http.StatusBadRequest: "INVALID_ARGUMENT", http.StatusUnauthorized: "UNAUTHENTICATED",
	http.StatusPaymentRequired: "RESOURCE_EXHAUSTED", http.StatusForbidden: "PERMISSION_DENIED",
	http.StatusNotFound: "NOT_FOUND", http.StatusMethodNotAllowed: "UNIMPLEMENTED", http.StatusConflict: "ABORTED",
	http.StatusRequestEntityTooLarge: "INVALID_ARGUMENT", http.StatusTooManyRequests: "RESOURCE_EXHAUSTED",
	499: "CANCELLED", http.StatusInternalServerError: "INTERNAL", http.StatusNotImplemented: "UNIMPLEMENTED",
	http.StatusBadGateway: "UNAVAILABLE", http.StatusServiceUnavailable: "UNAVAILABLE",
	http.StatusGatewayTimeout: "DEADLINE_EXCEEDED",
}

// Status is the status a Gemini client gets for the gateway's: Anthropic's
// 529 (overloaded) is Google's 503.
func Status(status int) int {
	if status == 529 {
		return http.StatusServiceUnavailable
	}
	return status
}

// Error is an error in Gemini's shape, for a status (Status's) and message.
func Error(status int, msg string) []byte {
	name, ok := statuses[status]
	if !ok {
		name = "UNKNOWN"
		if status >= 500 {
			name = "INTERNAL"
		}
	}
	out, _ := marshal(map[string]any{"error": map[string]any{"code": status, "message": msg, "status": name}})
	return out
}

// ErrorBody turns an error answer (the gateway's in OpenAI's shape, or a
// provider's) into Gemini's, keeping its message.
func ErrorBody(status int, body []byte) []byte {
	return Error(Status(status), errorMessage(status, body))
}

// errorMessage is an error answer's message: {"error":{"message"}}, the
// same in a list (Google's OpenAI-compatible API), or the status's text.
func errorMessage(status int, body []byte) string {
	type detail struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var one detail
	if json.Unmarshal(body, &one) == nil && one.Error != nil && one.Error.Message != "" {
		return one.Error.Message
	}
	var list []detail
	if json.Unmarshal(body, &list) == nil && len(list) > 0 && list[0].Error != nil && list[0].Error.Message != "" {
		return list[0].Error.Message
	}
	return "the gateway answered " + http.StatusText(status)
}

// streamStatus is the status of an error event mid-stream, by its type.
func streamStatus(typ string) int {
	switch typ {
	case "rate_limit_error", "rate_limit_exceeded", "insufficient_quota":
		return http.StatusTooManyRequests
	case "overloaded_error":
		return http.StatusServiceUnavailable
	case "invalid_request_error":
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// sortedKeys are a map's int keys, in order.
func sortedKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
