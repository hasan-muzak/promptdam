package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// Calls are answered by providers of the other API format, translated
// both ways (features/translate).

func anthropicTarget(base string) gateway.Target {
	return gateway.Target{Provider: "anthropic", Kind: "anthropic", BaseURL: base + "/v1", Model: "claude-haiku-4-5",
		Format: wire.AnthropicMessages, Header: http.Header{"X-Api-Key": {"sk-ant-provider"}}}
}

func openAITarget(base string) gateway.Target {
	return gateway.Target{Provider: "openai", Kind: "openai", BaseURL: base + "/v1", Model: "gpt-5-mini",
		Format: wire.OpenAIChat, Header: http.Header{"Authorization": {"Bearer sk-provider"}}}
}

func TestChatCallAnsweredByAnthropic(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{anthropicTarget(base)} })
	res, body := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","seed":3,
		"messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	var cc struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &cc); err != nil || cc.Object != "chat.completion" || cc.Choices[0].Message.Content != answer ||
		cc.Choices[0].FinishReason != "stop" || cc.Usage.PromptTokens != 175 {
		t.Fatalf("answer %s (%v)", body, err)
	}
	if res.Header.Get(brand.HeaderDropped) != "seed" || res.Header.Get("Content-Length") != "" && res.Header.Get("Content-Length") != itoa(len(body)) {
		t.Fatalf("headers %v", res.Header)
	}
	up := e.fake.Requests()[0]
	var sent struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	_ = json.Unmarshal(up.Body, &sent)
	if up.Path != "/messages" || up.Header.Get("Anthropic-Version") == "" || up.Header.Get("X-Api-Key") != "sk-ant-provider" ||
		sent.Model != "claude-haiku-4-5" || sent.MaxTokens == 0 || len(sent.System) != 1 || sent.System[0].Text != "Be brief." {
		t.Fatalf("upstream %s %v %s", up.Path, up.Header, up.Body)
	}
	rec := e.record(t)
	if !rec.Translated || !rec.UsageKnown || rec.Usage.Input != 175 || rec.Usage.CacheRead != 50 || rec.ServedModel != "claude-haiku-4-5" ||
		len(rec.Dropped) != 1 {
		t.Fatalf("record %+v", rec)
	}
}

func TestMessagesCallAnsweredByOpenAI(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{openAITarget(base)} })
	res, body := e.post(t, "/v1/messages", `{"model":"claude-opus-5-5","max_tokens":64,"top_k":4,
		"messages":[{"role":"user","content":"hi"}]}`, "Anthropic-Version", "2023-06-01", "Anthropic-Beta", "x")
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, `"type":"message"`) || !strings.Contains(body, `"text":"`+answer+`"`) ||
		!strings.Contains(body, `"stop_reason":"end_turn"`) || !strings.Contains(body, `"input_tokens":60`) ||
		!strings.Contains(body, `"cache_read_input_tokens":40`) || res.Header.Get(brand.HeaderDropped) != "top_k" {
		t.Fatalf("answer %s %v", body, res.Header)
	}
	up := e.fake.Requests()[0]
	if up.Path != "/chat/completions" || up.Header.Get("Anthropic-Version") != "" || up.Header.Get("Anthropic-Beta") != "" ||
		!strings.Contains(string(up.Body), `"max_completion_tokens":64`) {
		t.Fatalf("upstream %s %v %s", up.Path, up.Header, up.Body)
	}
}

func TestTranslatedStreams(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{anthropicTarget(base)} })
	_, body := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	var content strings.Builder
	usage := false
	for _, ev := range splitEvents(body) {
		if ev == "[DONE]" {
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage any `json:"usage"`
		}
		_ = json.Unmarshal([]byte(ev), &c)
		usage = usage || c.Usage != nil
		for _, ch := range c.Choices {
			content.WriteString(ch.Delta.Content)
		}
	}
	// The client didn't ask for the usage chunk: it gets none, but the
	// gateway still counts the call.
	if content.String() != answer || usage || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream %q", body)
	}
	if rec := e.record(t); !rec.Translated || !rec.UsageKnown || rec.Usage.Output != 10 {
		t.Fatalf("record %+v", rec)
	}

	e = setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{openAITarget(base)} })
	_, body = e.post(t, "/v1/messages", `{"model":"claude","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, "event: message_start") || !strings.HasSuffix(body, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") ||
		strings.Count(body, "text_delta") != 10 {
		t.Fatalf("stream %q", body)
	}
	if up := e.fake.Requests()[0]; !strings.Contains(string(up.Body), `"include_usage":true`) {
		t.Fatalf("upstream %s", up.Body)
	}
	if rec := e.record(t); !rec.UsageKnown || rec.Usage.Output != 10 {
		t.Fatalf("record %+v", rec)
	}
}

func splitEvents(stream string) []string {
	var out []string
	for ev := range strings.SplitSeq(strings.TrimSpace(stream), "\n\n") {
		for line := range strings.SplitSeq(ev, "\n") {
			if d, ok := strings.CutPrefix(line, "data: "); ok {
				out = append(out, d)
			}
		}
	}
	return out
}

func TestUntranslatableCallsTryTheNextTarget(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		same := openAITarget(base)
		same.Format = 0
		return gateway.Static{anthropicTarget(base), same}
	})
	res, body := e.post(t, "/v1/chat/completions", `{"model":"gpt-5","n":2,"messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 200 || res.Header.Get(brand.HeaderServedModel) != "gpt-5-mini" {
		t.Fatalf("%d %s %v", res.StatusCode, body, res.Header)
	}
	if n := len(e.fake.Requests()); n != 1 {
		t.Fatalf("the provider got %d calls", n)
	}

	e = setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{anthropicTarget(base)} })
	res, body = e.post(t, "/v1/chat/completions", `{"model":"gpt-5","n":2,"messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 400 || res.Header.Get(brand.HeaderErrorCode) != "untranslatable_request" || !strings.Contains(body, "several answers") {
		t.Fatalf("%d %s %v", res.StatusCode, body, res.Header)
	}
}

func TestProviderErrorsTakeTheClientsShape(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{openAITarget(base)} })
	e.fake.Fail(http.StatusBadRequest)
	res, body := e.post(t, "/v1/messages", `{"model":"claude","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 400 || body != `{"type":"error","error":{"type":"invalid_request_error","message":"fake failure"}}` {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if rec := e.record(t); rec.ErrorCode != "provider_400" {
		t.Fatalf("record %+v", rec)
	}
}

// The official SDKs read translated answers, streamed or not.
func TestSDKsAcrossFormats(t *testing.T) {
	ctx := context.Background()
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{anthropicTarget(base)} })
	oc := openai.NewClient(option.WithBaseURL(e.gateway.URL+"/v1/"), option.WithAPIKey("pd_sk_test_client"),
		option.WithUnsafeAllowHTTP(), option.WithMaxRetries(0))
	params := openai.ChatCompletionNewParams{Model: "gpt-5", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}}
	completion, err := oc.Chat.Completions.New(ctx, params)
	if err != nil || completion.Choices[0].Message.Content != answer {
		t.Fatalf("completion %+v, %v", completion, err)
	}
	stream := oc.Chat.Completions.NewStreaming(ctx, params)
	var acc openai.ChatCompletionAccumulator
	for stream.Next() {
		acc.AddChunk(stream.Current())
	}
	if err := stream.Err(); err != nil || acc.Choices[0].Message.Content != answer || acc.Usage.CompletionTokens != 10 {
		t.Fatalf("streamed %+v, %v", acc, err)
	}

	e = setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{openAITarget(base)} })
	ac := anthropic.NewClient(anthropicoption.WithBaseURL(e.gateway.URL), anthropicoption.WithAPIKey("pd_sk_test_client"),
		anthropicoption.WithMaxRetries(0))
	mp := anthropic.MessageNewParams{Model: "claude-opus-5-5", MaxTokens: 64,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))}}
	msg, err := ac.Messages.New(ctx, mp)
	if err != nil || len(msg.Content) != 1 || msg.Content[0].Text != answer || msg.Usage.OutputTokens != 10 {
		t.Fatalf("message %+v, %v", msg, err)
	}
	ms := ac.Messages.NewStreaming(ctx, mp)
	var m anthropic.Message
	for ms.Next() {
		if err := m.Accumulate(ms.Current()); err != nil {
			t.Fatal(err)
		}
	}
	if err := ms.Err(); err != nil || len(m.Content) != 1 || m.Content[0].Text != answer || m.StopReason != "end_turn" {
		t.Fatalf("streamed %+v, %v", m, err)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
