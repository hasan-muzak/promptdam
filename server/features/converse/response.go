package converse

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The Converse answer.
type cResponse struct {
	Output struct {
		Message struct {
			Content []cOutBlock `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      cUsage `json:"usage"`
}

type cOutBlock struct {
	Text    *string `json:"text"`
	ToolUse *struct {
		ToolUseID string          `json:"toolUseId"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
	} `json:"toolUse"`
	ReasoningContent *struct {
		ReasoningText *struct {
			Text      string `json:"text"`
			Signature string `json:"signature"`
		} `json:"reasoningText"`
		RedactedContent string `json:"redactedContent"`
	} `json:"reasoningContent"`
}

// cUsage is Converse's usage: inputTokens excludes what was read from and
// written to the cache, as Anthropic's input_tokens does.
type cUsage struct {
	InputTokens           int64 `json:"inputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	CacheReadInputTokens  int64 `json:"cacheReadInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	CacheDetails          []struct {
		InputTokens int64  `json:"inputTokens"`
		TTL         string `json:"ttl"`
	} `json:"cacheDetails"`
}

// messagesUsage is Anthropic's usage for Converse's.
func (u cUsage) messagesUsage() map[string]any {
	out := map[string]any{"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens,
		"cache_read_input_tokens": u.CacheReadInputTokens, "cache_creation_input_tokens": u.CacheWriteInputTokens}
	var hour, five int64
	for _, d := range u.CacheDetails {
		if d.TTL == "1h" {
			hour += d.InputTokens
		} else {
			five += d.InputTokens
		}
	}
	if hour > 0 {
		out["cache_creation"] = map[string]int64{"ephemeral_1h_input_tokens": hour, "ephemeral_5m_input_tokens": five}
	}
	return out
}

// stopReason is Anthropic's stop reason for Converse's.
func stopReason(s string) string {
	switch s {
	case "end_turn", "tool_use", "max_tokens", "stop_sequence", "model_context_window_exceeded":
		return s
	case "guardrail_intervened", "content_filtered":
		return "refusal"
	}
	return "end_turn" // malformed output: what was written ends the turn
}

// MessageID is an answer's id, from the request Bedrock names in its
// x-amzn-requestid header.
func MessageID(h http.Header) string {
	if id := h.Get("X-Amzn-Requestid"); id != "" {
		return "msg_bdrk_" + strings.ReplaceAll(id, "-", "")
	}
	return "msg_bdrk"
}

// Response turns a Converse answer into a Messages answer, by model (the
// Bedrock id it was asked of) and id (MessageID).
func Response(body []byte, model, id string) ([]byte, error) {
	var in cResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	content := []map[string]any{}
	for _, b := range in.Output.Message.Content {
		switch {
		case b.Text != nil:
			content = append(content, map[string]any{"type": "text", "text": *b.Text})
		case b.ToolUse != nil:
			input := b.ToolUse.Input
			if len(input) == 0 || string(input) == "null" {
				input = json.RawMessage(`{}`)
			}
			content = append(content, map[string]any{"type": "tool_use", "id": b.ToolUse.ToolUseID, "name": b.ToolUse.Name,
				"input": input})
		case b.ReasoningContent != nil && b.ReasoningContent.ReasoningText != nil:
			r := b.ReasoningContent.ReasoningText
			content = append(content, map[string]any{"type": "thinking", "thinking": r.Text, "signature": r.Signature})
		case b.ReasoningContent != nil && b.ReasoningContent.RedactedContent != "":
			content = append(content, map[string]any{"type": "redacted_thinking", "data": b.ReasoningContent.RedactedContent})
		}
	}
	return json.Marshal(map[string]any{"id": id, "type": "message", "role": "assistant", "model": model, "content": content,
		"stop_reason": stopReason(in.StopReason), "stop_sequence": nil, "usage": in.Usage.messagesUsage()})
}

// Error turns an AWS error answer (a JSON {"message"} and its type in the
// X-Amzn-ErrorType header) into Anthropic's, and the status the gateway
// acts on: a model's own error (424) is the provider's failure, a 502, so
// fallbacks take the call.
func Error(status int, h http.Header, body []byte) (int, []byte) {
	var e struct {
		Message  string `json:"message"`
		MessageU string `json:"Message"`
		Type     string `json:"__type"`
	}
	_ = json.Unmarshal(body, &e)
	msg := e.Message
	if msg == "" {
		msg = e.MessageU
	}
	kind := h.Get("X-Amzn-Errortype")
	if kind == "" {
		kind = e.Type
	}
	kind, _, _ = strings.Cut(kind, ":")
	if i := strings.LastIndexByte(kind, '#'); i >= 0 {
		kind = kind[i+1:]
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	if kind != "" {
		msg = kind + ": " + msg
	}
	if status == http.StatusFailedDependency {
		status = http.StatusBadGateway
	}
	out, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": errorType(status, kind), "message": msg}})
	return status, out
}

// errorType is Anthropic's error type for a status (and AWS's type).
func errorType(status int, kind string) string {
	switch {
	case kind == "ThrottlingException" || kind == "ModelNotReadyException" || status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status == http.StatusBadRequest:
		return "invalid_request_error"
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status == http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case status == http.StatusServiceUnavailable:
		return "overloaded_error"
	}
	return "api_error"
}
