package gateway_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// The official SDKs work against the gateway with nothing changed but
// their base URL: the S0 exit.

const answer = "tok0 tok1 tok2 tok3 tok4 tok5 tok6 tok7 tok8 tok9 "

func TestOpenAISDK(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, nil)
	client := openai.NewClient(option.WithBaseURL(e.gateway.URL+"/v1/"), option.WithAPIKey("pd_sk_test_client"),
		option.WithUnsafeAllowHTTP(), option.WithMaxRetries(0))
	ctx := context.Background()
	params := openai.ChatCompletionNewParams{
		Model:    "gpt-5-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	}

	completion, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if completion.Choices[0].Message.Content != answer || completion.Usage.PromptTokens != fakeupstream.PromptTokens {
		t.Fatalf("completion %+v", completion)
	}

	stream := client.Chat.Completions.NewStreaming(ctx, params)
	var acc openai.ChatCompletionAccumulator
	for stream.Next() {
		acc.AddChunk(stream.Current())
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if acc.Choices[0].Message.Content != answer {
		t.Fatalf("streamed %q", acc.Choices[0].Message.Content)
	}
	if rec := e.record(t); !rec.Stream || !rec.UsageKnown || rec.Usage.Output != 10 {
		t.Fatalf("record %+v", rec)
	}

	resp, err := client.Responses.New(ctx, responses.ResponseNewParams{
		Model: "gpt-5",
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputText() != answer {
		t.Fatalf("response %q", resp.OutputText())
	}
	rstream := client.Responses.NewStreaming(ctx, responses.ResponseNewParams{
		Model: "gpt-5",
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("hi")},
	})
	var text strings.Builder
	for rstream.Next() {
		if ev := rstream.Current(); ev.Type == "response.output_text.delta" {
			text.WriteString(ev.Delta)
		}
	}
	if err := rstream.Err(); err != nil || text.String() != answer {
		t.Fatalf("streamed %q, %v", text.String(), err)
	}

	// The gateway's own errors reach the SDK as API errors with their code.
	e = setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return refuse{&gateway.Error{Status: 401, Type: gateway.TypeAuthentication, Code: "invalid_api_key", Message: "no"}}
	})
	client = openai.NewClient(option.WithBaseURL(e.gateway.URL+"/v1/"), option.WithAPIKey("pd_sk_test_client"),
		option.WithUnsafeAllowHTTP(), option.WithMaxRetries(0))
	_, err = client.Chat.Completions.New(ctx, params)
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 || apiErr.Code != "invalid_api_key" {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicSDK(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		return gateway.Static{{Provider: "anthropic", BaseURL: base + "/v1", Header: http.Header{"X-Api-Key": {"sk-ant-provider"}}}}
	})
	// Claude Code and the SDKs take the base URL without /v1.
	client := anthropic.NewClient(anthropicoption.WithBaseURL(e.gateway.URL), anthropicoption.WithAPIKey("pd_sk_test_client"),
		anthropicoption.WithMaxRetries(0))
	ctx := context.Background()
	params := anthropic.MessageNewParams{
		Model:     "claude-haiku-4-5",
		MaxTokens: 64,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	}

	msg, err := client.Messages.New(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content[0].Text != answer || msg.Usage.CacheReadInputTokens != fakeupstream.CacheReadTokens {
		t.Fatalf("message %+v", msg)
	}

	stream := client.Messages.NewStreaming(ctx, params)
	var acc anthropic.Message
	for stream.Next() {
		if err := acc.Accumulate(stream.Current()); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if acc.Content[0].Text != answer || acc.Usage.OutputTokens != 10 {
		t.Fatalf("streamed %+v", acc)
	}
	if up := e.fake.Requests()[1]; up.Header.Get("X-Api-Key") != "sk-ant-provider" {
		t.Fatalf("the provider got %q", up.Header.Get("X-Api-Key"))
	}

	count, err := client.Messages.CountTokens(ctx, anthropic.MessageCountTokensParams{
		Model:    "claude-haiku-4-5",
		Messages: params.Messages,
	})
	if err != nil || count.InputTokens != fakeupstream.PromptTokens {
		t.Fatalf("count %+v, %v", count, err)
	}
}
