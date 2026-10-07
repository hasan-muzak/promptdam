package translate

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

type msgUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type messagesAnswer struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Role         string     `json:"role"`
	Model        string     `json:"model"`
	Content      []msgBlock `json:"content"`
	StopReason   string     `json:"stop_reason"`
	StopSequence *string    `json:"stop_sequence"`
	Usage        msgUsage   `json:"usage"`
}

type chatUsage struct {
	PromptTokens        int64         `json:"prompt_tokens"`
	CompletionTokens    int64         `json:"completion_tokens"`
	TotalTokens         int64         `json:"total_tokens"`
	PromptTokensDetails *tokenDetails `json:"prompt_tokens_details,omitempty"`
}

type tokenDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type chatCompletion struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type chatChoice struct {
	Index        int        `json:"index"`
	Message      chatAnswer `json:"message"`
	FinishReason string     `json:"finish_reason"`
	Logprobs     any        `json:"logprobs"`
}

type chatAnswer struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	Refusal   *string        `json:"refusal"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   *string        `json:"content"`
			Refusal   *string        `json:"refusal"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// Response translates a whole answer of the provider's format into the
// client's.
func Response(provider, client wire.Format, body []byte) ([]byte, error) {
	switch {
	case provider == wire.AnthropicMessages && client == wire.OpenAIChat:
		var in messagesAnswer
		if json.Unmarshal(body, &in) != nil {
			return nil, refuse("the provider's answer is not a Messages response")
		}
		return marshal(completionOf(in, time.Now()))
	case provider == wire.OpenAIChat && client == wire.AnthropicMessages:
		var in chatResponse
		if json.Unmarshal(body, &in) != nil {
			return nil, refuse("the provider's answer is not a Chat Completions response")
		}
		return marshal(messageOf(in))
	}
	return nil, refuse("answers of " + provider.String() + " can't be sent as " + client.String())
}

func completionOf(in messagesAnswer, now time.Time) chatCompletion {
	var answer chatAnswer
	answer.Role = "assistant"
	var s []byte
	for _, b := range in.Content {
		switch b.Type {
		case "text":
			if b.Text != nil {
				s = append(s, *b.Text...)
			}
		case "tool_use":
			args := string(b.Input)
			if null(b.Input) {
				args = "{}"
			}
			answer.ToolCalls = append(answer.ToolCalls, chatToolCall{ID: b.ID, Type: "function",
				Function: chatFunction{Name: b.Name, Arguments: args}})
		}
	}
	if len(s) > 0 || len(answer.ToolCalls) == 0 {
		answer.Content = text(string(s))
	}
	if in.StopReason == "refusal" {
		answer.Refusal, answer.Content = answer.Content, nil
	}
	return chatCompletion{
		ID: in.ID, Object: "chat.completion", Created: now.Unix(), Model: in.Model,
		Choices: []chatChoice{{Message: answer, FinishReason: finishReason(in.StopReason)}},
		Usage:   chatUsageOf(in.Usage),
	}
}

func messageOf(in chatResponse) messagesAnswer {
	out := messagesAnswer{ID: in.ID, Type: "message", Role: "assistant", Model: in.Model, Content: []msgBlock{}, StopReason: "end_turn"}
	if len(in.Choices) > 0 {
		c := in.Choices[0]
		out.StopReason = stopReason(c.FinishReason)
		switch {
		case c.Message.Content != nil && *c.Message.Content != "":
			out.Content = append(out.Content, msgBlock{Type: "text", Text: c.Message.Content})
		case c.Message.Refusal != nil && *c.Message.Refusal != "":
			out.Content = append(out.Content, msgBlock{Type: "text", Text: c.Message.Refusal})
			out.StopReason = "refusal"
		}
		for _, tc := range c.Message.ToolCalls {
			out.Content = append(out.Content, msgBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: toolInput(tc.Function.Arguments)})
		}
	}
	if in.Usage != nil {
		out.Usage = msgUsageOf(*in.Usage)
	}
	return out
}

// chatUsageOf counts Messages usage the Chat way: prompt tokens include
// those read from or written to the cache.
func chatUsageOf(u msgUsage) *chatUsage {
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	out := &chatUsage{PromptTokens: prompt, CompletionTokens: u.OutputTokens, TotalTokens: prompt + u.OutputTokens}
	if u.CacheReadInputTokens > 0 {
		out.PromptTokensDetails = &tokenDetails{CachedTokens: u.CacheReadInputTokens}
	}
	return out
}

// msgUsageOf counts Chat usage the Messages way: input tokens leave out
// those read from the cache.
func msgUsageOf(u chatUsage) msgUsage {
	var cached int64
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
	}
	return msgUsage{InputTokens: max(u.PromptTokens-cached, 0), OutputTokens: u.CompletionTokens, CacheReadInputTokens: cached}
}

// finishReason is Chat's finish_reason for a Messages stop_reason.
func finishReason(stop string) string {
	switch stop {
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	}
	return "stop"
}

// stopReason is the Messages stop_reason for a Chat finish_reason.
func stopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	}
	return "end_turn"
}

// ErrorBody translates a provider's error answer into the client's error
// shape, keeping its message. A body it can't read says what the status
// means.
func ErrorBody(client wire.Format, status int, body []byte) []byte {
	var in struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	msg, typ := "", errorType(status)
	if json.Unmarshal(body, &in) == nil && in.Error != nil {
		msg = in.Error.Message
		if in.Error.Type != "" {
			typ = in.Error.Type
		}
	}
	if msg == "" {
		msg = "the provider answered " + strconv.Itoa(status) + " " + http.StatusText(status)
	}
	if client.Anthropic() {
		return anthropicError(errorType(status), msg)
	}
	return openAIError(typ, msg)
}

// anthropicError is an error in the Messages API's shape.
func anthropicError(typ, msg string) []byte {
	type detail struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	out, _ := marshal(struct {
		Type  string `json:"type"`
		Error detail `json:"error"`
	}{"error", detail{typ, msg}})
	return out
}

// openAIError is an error in the Chat Completions API's shape.
func openAIError(typ, msg string) []byte {
	type detail struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Param   any    `json:"param"`
	}
	out, _ := marshal(struct {
		Error detail `json:"error"`
	}{detail{Message: msg, Type: typ}})
	return out
}

// errorType names an error by its status, as Anthropic does.
func errorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status == http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status == 529 || status == http.StatusServiceUnavailable:
		return "overloaded_error"
	case status >= 500:
		return "api_error"
	}
	return "invalid_request_error"
}
