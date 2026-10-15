package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/auth/bearer"
	"google.golang.org/genai"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// Google's and AWS's own SDKs call the gateway through its doors: the
// request is converted at the edge, served as any call, and every answer
// goes back in the SDK's API.

// keyed is a Static resolver that keeps the key each call carried, as the
// resolver reads it, and lists the models it serves.
type keyed struct {
	gateway.Static
	mu   sync.Mutex
	keys []string
}

func (k *keyed) Resolve(ctx context.Context, c *gateway.Call) (*gateway.Route, error) {
	k.mu.Lock()
	k.keys = append(k.keys, c.Header.Get("X-Api-Key")+"|"+c.Header.Get("Authorization"))
	k.mu.Unlock()
	return k.Static.Resolve(ctx, c)
}

func (k *keyed) Models(context.Context, *gateway.Call) ([]gateway.Model, error) {
	return []gateway.Model{{ID: "gemini-3.5-flash", Provider: "gemini"}, {ID: "claude-haiku-4-5", Provider: "anthropic"},
		{ID: "gemini-3.5-flash", Provider: "vertex"}}, nil
}

func (k *keyed) last() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.keys) == 0 {
		return ""
	}
	return k.keys[len(k.keys)-1]
}

func geminiClient(t *testing.T, base string) *genai.Client {
	t.Helper()
	c, err := genai.NewClient(context.Background(), &genai.ClientConfig{APIKey: "pd_sk_test_client", Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: base + "/gemini/"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Google's SDK reaches a Chat Completions provider: answers, streams,
// embeddings and the model list in Gemini's shape, its key read from
// x-goog-api-key, the call logged as the Gemini door's.
func TestGeminiSDK(t *testing.T) {
	var k *keyed
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		k = &keyed{Static: gateway.Static{{Provider: "gemini", Kind: "gemini", BaseURL: base + "/v1",
			Header: http.Header{"Authorization": {"Bearer provider-key"}}}}}
		return k
	})
	client := geminiClient(t, e.gateway.URL)
	ctx := context.Background()
	temp := float32(0.2)
	resp, err := client.Models.GenerateContent(ctx, "gemini-3.5-flash", genai.Text("hi"), &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText("Be brief.", genai.RoleUser), Temperature: &temp, MaxOutputTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	u := resp.UsageMetadata
	if resp.Text() != "tok0 tok1 tok2 tok3 tok4 tok5 tok6 tok7 tok8 tok9 " || resp.ModelVersion != "gemini-3.5-flash" ||
		u == nil || u.PromptTokenCount != fakeupstream.PromptTokens || u.CachedContentTokenCount != fakeupstream.CachedTokens ||
		u.CandidatesTokenCount != 10 || resp.Candidates[0].FinishReason != genai.FinishReasonStop {
		t.Fatalf("answer %+v", resp)
	}
	if k.last() != "pd_sk_test_client|" {
		t.Fatalf("key %q", k.last())
	}
	sent := e.fake.Requests()[0]
	var chat struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(sent.Body, &chat); err != nil || sent.Path != "/chat/completions" || chat.Model != "gemini-3.5-flash" ||
		chat.MaxTokens != 64 || len(chat.Messages) != 2 || chat.Messages[0].Role != "system" || chat.Messages[1].Content != "hi" {
		t.Fatalf("sent %s %s", sent.Path, sent.Body)
	}
	if sent.Header.Get("Authorization") != "Bearer provider-key" || sent.Header.Get("X-Goog-Api-Key") != "" {
		t.Fatalf("provider headers %v", sent.Header)
	}
	rec := e.record(t)
	if rec.Door != gateway.DoorGemini || rec.Format != wire.OpenAIChat || rec.Status != 200 || !rec.UsageKnown {
		t.Fatalf("record %+v", rec)
	}

	// Streamed, as server-sent events (the SDK asks alt=sse), the usage on
	// the last chunk.
	var text strings.Builder
	var last *genai.GenerateContentResponse
	for chunk, err := range client.Models.GenerateContentStream(ctx, "gemini-3.5-flash", genai.Text("hi"), nil) {
		if err != nil {
			t.Fatal(err)
		}
		text.WriteString(chunk.Text())
		last = chunk
	}
	if text.String() != "tok0 tok1 tok2 tok3 tok4 tok5 tok6 tok7 tok8 tok9 " || last.UsageMetadata == nil ||
		last.UsageMetadata.PromptTokenCount != fakeupstream.PromptTokens || last.Candidates[0].FinishReason != genai.FinishReasonStop {
		t.Fatalf("stream %q, last %+v", text.String(), last)
	}
	if rec := e.record(t); !rec.Stream || !rec.UsageKnown || rec.Door != gateway.DoorGemini {
		t.Fatalf("stream record %+v", rec)
	}

	// Embeddings, as batchEmbedContents.
	emb, err := client.Models.EmbedContent(ctx, "text-embedding-005", genai.Text("hello"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.Embeddings) != 1 || len(emb.Embeddings[0].Values) != 3 || emb.Embeddings[0].Values[1] != 0.2 {
		t.Fatalf("embeddings %+v", emb)
	}

	// The model list, each model once.
	page, err := client.Models.List(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Name != "models/gemini-3.5-flash" {
		t.Fatalf("models %+v", page.Items)
	}
	m, err := client.Models.Get(ctx, "claude-haiku-4-5", nil)
	if err != nil || m.Name != "models/claude-haiku-4-5" {
		t.Fatalf("model %+v %v", m, err)
	}
}

// A Gemini call is answered by Claude, through the translator; a
// provider's error reaches the SDK in Google's shape.
func TestGeminiSDKToClaude(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{anthropicTarget(base)} })
	client := geminiClient(t, e.gateway.URL)
	ctx := context.Background()
	resp, err := client.Models.GenerateContent(ctx, "claude-haiku-4-5", genai.Text("hi"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != answer || resp.UsageMetadata.PromptTokenCount != 175 || resp.UsageMetadata.CachedContentTokenCount != 50 {
		t.Fatalf("answer %q %+v", resp.Text(), resp.UsageMetadata)
	}
	var text strings.Builder
	for chunk, err := range client.Models.GenerateContentStream(ctx, "claude-haiku-4-5", genai.Text("hi"), nil) {
		if err != nil {
			t.Fatal(err)
		}
		text.WriteString(chunk.Text())
	}
	if text.String() != answer {
		t.Fatalf("stream %q", text.String())
	}
	if rec := e.record(t); rec.Door != gateway.DoorGemini || !rec.Translated || !rec.UsageKnown {
		t.Fatalf("record %+v", rec)
	}

	e.fake.Fail(400)
	_, err = client.Models.GenerateContent(ctx, "claude-haiku-4-5", genai.Text("hi"), nil)
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 400 || apiErr.Status != "INVALID_ARGUMENT" || apiErr.Message != "fake failure" {
		t.Fatalf("error %#v", err)
	}
}

// chatTools is a Chat provider that calls a tool, signing the call as
// Gemini 3 does, and keeps what it was sent.
type chatTools struct {
	mu   sync.Mutex
	sent [][]byte
}

func (c *chatTools) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.sent = append(c.sent, body)
	c.mu.Unlock()
	if strings.Contains(string(body), `"stream":true`) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, d := range []string{
			`{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x",
				"type":"function","function":{"name":"rate","arguments":"{\"hs\":"},"extra_content":{"google":{"thought_signature":"c2ln"}}}]}}]}`,
			`{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"8471\"}"}}]}}]}`,
			`{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"id":"c1","model":"gemini-3.5-flash","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`,
			`[DONE]`,
		} {
			_, _ = io.WriteString(w, "data: "+strings.ReplaceAll(strings.ReplaceAll(d, "\n", ""), "\t", "")+"\n\n")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"gemini-3.5-flash","choices":[{"index":0,
		"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_x","type":"function",
		"function":{"name":"rate","arguments":"{\"hs\":\"8471\"}"},"extra_content":{"google":{"thought_signature":"c2ln"}}}]}}],
		"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
}

// Function calling round trips through the door: the call comes back with
// its id and thought signature, whole when streamed, and the SDK's answer
// to it reaches the provider as a tool message, the signature kept.
func TestGeminiSDKTools(t *testing.T) {
	tools := &chatTools{}
	up := httptest.NewServer(tools)
	t.Cleanup(up.Close)
	e := setup(t, fakeupstream.Options{}, func(string) gateway.Resolver {
		return gateway.Static{{Provider: "gemini", Kind: "gemini", BaseURL: up.URL + "/v1"}}
	})
	client := geminiClient(t, e.gateway.URL)
	ctx := context.Background()
	cfg := &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
		Name: "rate", Description: "Duty rate of an HS code",
		Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"hs": {Type: genai.TypeString}},
			Required: []string{"hs"}}}}}}}
	question := genai.Text("Duty on laptops?")
	resp, err := client.Models.GenerateContent(ctx, "gemini-3.5-flash", question, cfg)
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.FunctionCalls()
	part := resp.Candidates[0].Content.Parts[0]
	if len(calls) != 1 || calls[0].Name != "rate" || calls[0].ID != "call_x" || calls[0].Args["hs"] != "8471" ||
		string(part.ThoughtSignature) != "sig" {
		t.Fatalf("calls %+v, part %+v", calls, part)
	}
	var streamed []*genai.FunctionCall
	for chunk, err := range client.Models.GenerateContentStream(ctx, "gemini-3.5-flash", question, cfg) {
		if err != nil {
			t.Fatal(err)
		}
		streamed = append(streamed, chunk.FunctionCalls()...)
	}
	if len(streamed) != 1 || streamed[0].Args["hs"] != "8471" || streamed[0].ID != "call_x" {
		t.Fatalf("streamed calls %+v", streamed)
	}

	answer := genai.NewPartFromFunctionResponse("rate", map[string]any{"rate": "0%"})
	answer.FunctionResponse.ID = calls[0].ID
	history := append(question, resp.Candidates[0].Content, genai.NewContentFromParts([]*genai.Part{answer}, genai.RoleUser))
	if _, err := client.Models.GenerateContent(ctx, "gemini-3.5-flash", history, cfg); err != nil {
		t.Fatal(err)
	}
	tools.mu.Lock()
	last := tools.sent[len(tools.sent)-1]
	tools.mu.Unlock()
	var chat struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID    string `json:"id"`
				Extra struct {
					Google struct {
						ThoughtSignature string `json:"thought_signature"`
					} `json:"google"`
				} `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Parameters map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(last, &chat); err != nil || len(chat.Messages) != 3 {
		t.Fatalf("sent %s", last)
	}
	m := chat.Messages
	if m[1].Role != "assistant" || m[1].ToolCalls[0].ID != "call_x" || m[1].ToolCalls[0].Extra.Google.ThoughtSignature != "c2ln" ||
		m[2].Role != "tool" || m[2].ToolCallID != "call_x" || m[2].Content != `{"rate":"0%"}` ||
		chat.Tools[0].Function.Parameters["type"] != "object" {
		t.Fatalf("sent %s", last)
	}
}

// Without alt=sse Google streams a JSON array; a key may come as ?key=;
// what the door can't serve is refused in Google's shape, before any
// provider is asked.
func TestGeminiDoorRaw(t *testing.T) {
	var k *keyed
	e := setup(t, fakeupstream.Options{Chunks: 2}, func(base string) gateway.Resolver {
		k = &keyed{Static: gateway.Static{{Provider: "gemini", Kind: "gemini", BaseURL: base + "/v1"}}}
		return k
	})
	call := func(path, body string) (reply, string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.gateway.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return reply{StatusCode: res.StatusCode, Header: res.Header}, string(out)
	}
	res, body := call("/gemini/v1beta/models/gemini-3.5-flash:streamGenerateContent?key=pd_sk_query",
		`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	var chunks []json.RawMessage
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json" || json.Unmarshal([]byte(body), &chunks) != nil ||
		len(chunks) != 3 {
		t.Fatalf("%d %s %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	if k.last() != "pd_sk_query|" {
		t.Fatalf("key %q", k.last())
	}
	asked := len(e.fake.Requests())
	res, body = call("/gemini/v1beta/models/gemini-3.5-flash:generateContent",
		`{"contents":[{"parts":[{"text":"news?"}]}],"tools":[{"googleSearch":{}}]}`)
	if res.StatusCode != 400 || res.Header.Get(brand.HeaderErrorCode) != "untranslatable_request" ||
		!strings.Contains(body, `"status":"INVALID_ARGUMENT"`) || !strings.Contains(body, "Google Search") || len(e.fake.Requests()) != asked {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	res, body = call("/gemini/v1beta/models/gemini-3.5-flash:predict", `{}`)
	if res.StatusCode != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

// bedrockGateway is the gateway over TLS (AWS's Go SDK sends a bearer
// token over HTTPS only) and a Bedrock runtime client on it, signing in
// with a Promptdam key as a Bedrock API key.
func bedrockGateway(t *testing.T, e *env) (*httptest.Server, *bedrockruntime.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(e.gateway.Config.Handler)
	t.Cleanup(srv.Close)
	client := bedrockruntime.New(bedrockruntime.Options{Region: "us-east-1", BaseEndpoint: aws.String(srv.URL + "/bedrock"),
		BearerAuthTokenProvider: bearer.StaticTokenProvider{Token: bearer.Token{Value: "pd_sk_test_client"}},
		AuthSchemePreference:    []string{"httpBearerAuth"}, HTTPClient: srv.Client(), RetryMaxAttempts: 1})
	return srv, client
}

func userText(s string) []types.Message {
	return []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: s}}}}
}

// AWS's SDK reaches a Messages provider through Converse and InvokeModel,
// whole and streamed: Converse's answer, events and usage, Claude's own
// answers for InvokeModel, errors as AWS's exceptions.
func TestBedrockSDK(t *testing.T) {
	var k *keyed
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
		k = &keyed{Static: gateway.Static{anthropicTarget(base)}}
		return k
	})
	_, client := bedrockGateway(t, e)
	ctx := context.Background()
	out, err := client.Converse(ctx, &bedrockruntime.ConverseInput{ModelId: aws.String("claude-haiku-4-5"), Messages: userText("hi"),
		System:          []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: "Be brief."}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(64)}})
	if err != nil {
		t.Fatal(err)
	}
	msg := out.Output.(*types.ConverseOutputMemberMessage).Value
	if msg.Content[0].(*types.ContentBlockMemberText).Value != answer || out.StopReason != types.StopReasonEndTurn ||
		aws.ToInt32(out.Usage.InputTokens) != fakeupstream.PromptTokens || aws.ToInt32(out.Usage.CacheReadInputTokens) != fakeupstream.CacheReadTokens ||
		aws.ToInt32(out.Usage.CacheWriteInputTokens) != fakeupstream.CacheWriteTokens || aws.ToInt32(out.Usage.OutputTokens) != 10 {
		t.Fatalf("answer %+v usage %+v", msg, out.Usage)
	}
	if k.last() != "|Bearer pd_sk_test_client" {
		t.Fatalf("key %q", k.last())
	}
	sent := e.fake.Requests()[0]
	var m struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	if err := json.Unmarshal(sent.Body, &m); err != nil || sent.Path != "/messages" || m.MaxTokens != 64 ||
		sent.Header.Get("Authorization") != "" || sent.Header.Get("X-Api-Key") != "sk-ant-provider" {
		t.Fatalf("sent %s %s %v", sent.Path, sent.Body, sent.Header)
	}
	if rec := e.record(t); rec.Door != gateway.DoorConverse || rec.Format != wire.AnthropicMessages || !rec.UsageKnown {
		t.Fatalf("record %+v", rec)
	}

	// Without maxTokens, the model's own limit (the translation default
	// for a model the price list doesn't know).
	if _, err := client.Converse(ctx, &bedrockruntime.ConverseInput{ModelId: aws.String("claude-haiku-4-5"), Messages: userText("hi")}); err != nil {
		t.Fatal(err)
	}
	reqs := e.fake.Requests()
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &m); err != nil || m.MaxTokens != 8192 {
		t.Fatalf("no limit: %s", reqs[len(reqs)-1].Body)
	}

	stream, err := client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{ModelId: aws.String("claude-haiku-4-5"),
		Messages: userText("hi")})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var stop types.StopReason
	var usage *types.TokenUsage
	es := stream.GetStream()
	for ev := range es.Events() {
		switch v := ev.(type) {
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			if d, ok := v.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				text.WriteString(d.Value)
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			stop = v.Value.StopReason
		case *types.ConverseStreamOutputMemberMetadata:
			usage = v.Value.Usage
		}
	}
	if err := es.Close(); err != nil || es.Err() != nil {
		t.Fatal(err, es.Err())
	}
	if text.String() != "tok0 tok1 tok2 tok3 tok4 tok5 tok6 tok7 tok8 tok9 " || stop != types.StopReasonEndTurn || usage == nil ||
		aws.ToInt32(usage.InputTokens) != fakeupstream.PromptTokens || aws.ToInt32(usage.OutputTokens) != 10 {
		t.Fatalf("stream %q %v %+v", text.String(), stop, usage)
	}

	// InvokeModel with Claude's own body, and its token-count headers.
	inv, err := client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{ModelId: aws.String("claude-haiku-4-5"),
		ContentType: aws.String("application/json"),
		Body:        []byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	var claude struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(inv.Body, &claude); err != nil || claude.Content[0].Text != answer {
		t.Fatalf("invoke %s", inv.Body)
	}
	if rec := e.record(t); rec.Door != gateway.DoorInvoke {
		t.Fatalf("record %+v", rec)
	}

	invStream, err := client.InvokeModelWithResponseStream(ctx, &bedrockruntime.InvokeModelWithResponseStreamInput{
		ModelId: aws.String("claude-haiku-4-5"), ContentType: aws.String("application/json"),
		Body: []byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	text.Reset()
	var metrics map[string]int64
	is := invStream.GetStream()
	for ev := range is.Events() {
		chunk, ok := ev.(*types.ResponseStreamMemberChunk)
		if !ok {
			continue
		}
		var e struct {
			Type  string `json:"type"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
			Metrics map[string]int64 `json:"amazon-bedrock-invocationMetrics"`
		}
		if err := json.Unmarshal(chunk.Value.Bytes, &e); err != nil {
			t.Fatal(err)
		}
		text.WriteString(e.Delta.Text)
		if e.Type == "message_stop" {
			metrics = e.Metrics
		}
	}
	if err := is.Close(); err != nil || is.Err() != nil {
		t.Fatal(err, is.Err())
	}
	if text.String() != "tok0 tok1 tok2 tok3 tok4 tok5 tok6 tok7 tok8 tok9 " || metrics["inputTokenCount"] != fakeupstream.PromptTokens {
		t.Fatalf("invoke stream %q %v", text.String(), metrics)
	}

	e.fake.Fail(400)
	_, err = client.Converse(ctx, &bedrockruntime.ConverseInput{ModelId: aws.String("claude-haiku-4-5"), Messages: userText("hi")})
	var ve *types.ValidationException
	if !errors.As(err, &ve) || aws.ToString(ve.Message) != "fake failure" {
		t.Fatalf("error %v", err)
	}
	_, err = client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{ModelId: aws.String("meta.llama3-3-70b-instruct-v1:0"),
		Body: []byte(`{"prompt":"hi"}`)})
	if !errors.As(err, &ve) || !strings.Contains(aws.ToString(ve.Message), "Converse") {
		t.Fatalf("a Llama body: %v", err)
	}
}

// A Converse call is answered by a Chat Completions provider, translated
// twice (Converse to Messages at the door, Messages to Chat for the
// provider), streamed too.
func TestBedrockSDKToChat(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{openAITarget(base)} })
	_, client := bedrockGateway(t, e)
	ctx := context.Background()
	out, err := client.Converse(ctx, &bedrockruntime.ConverseInput{ModelId: aws.String("gpt-5-mini"), Messages: userText("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Output.(*types.ConverseOutputMemberMessage).Value.Content[0].(*types.ContentBlockMemberText).Value; got !=
		"tok0 tok1 tok2 tok3 tok4 tok5 tok6 tok7 tok8 tok9 " || aws.ToInt32(out.Usage.InputTokens) != fakeupstream.PromptTokens-fakeupstream.CachedTokens ||
		aws.ToInt32(out.Usage.CacheReadInputTokens) != fakeupstream.CachedTokens {
		t.Fatalf("answer %q %+v", got, out.Usage)
	}
	if e.fake.Requests()[0].Path != "/chat/completions" {
		t.Fatalf("sent to %s", e.fake.Requests()[0].Path)
	}
	stream, err := client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{ModelId: aws.String("gpt-5-mini"), Messages: userText("hi")})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	es := stream.GetStream()
	for ev := range es.Events() {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if t, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				text.WriteString(t.Value)
			}
		}
	}
	if err := es.Close(); err != nil || es.Err() != nil || text.String() != "tok0 tok1 tok2 tok3 tok4 tok5 tok6 tok7 tok8 tok9 " {
		t.Fatalf("stream %q %v %v", text.String(), err, es.Err())
	}
}

// AWS credentials (SigV4) can't sign in: the client is told to send its
// Promptdam key as a Bedrock API key. Doubled slashes and escaped ids
// still reach the door.
func TestBedrockDoorRaw(t *testing.T) {
	e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver { return gateway.Static{anthropicTarget(base)} })
	call := func(path string, header ...string) (reply, string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.gateway.URL+path,
			strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`))
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return reply{StatusCode: res.StatusCode, Header: res.Header}, string(out)
	}
	res, body := call("/bedrock/model/claude-haiku-4-5/converse", "Authorization",
		"AWS4-HMAC-SHA256 Credential=AKIAXXXXXXXXXXXXXXXX/20261015/us-east-1/bedrock/aws4_request, SignedHeaders=host, Signature=abc")
	if res.StatusCode != 403 || res.Header.Get("X-Amzn-Errortype") != "AccessDeniedException" || !strings.Contains(body, "Bedrock API key") {
		t.Fatalf("%d %v %s", res.StatusCode, res.Header, body)
	}
	res, body = call("/bedrock//model/us.anthropic.claude-haiku-4-5-20251001-v1%3A0/converse", "Authorization", "Bearer pd_sk_test_client")
	if res.StatusCode != 200 || !strings.Contains(body, `"stopReason":"end_turn"`) || res.Header.Get("X-Amzn-Requestid") == "" {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if rec := e.record(t); rec.RequestedModel != "us.anthropic.claude-haiku-4-5-20251001-v1:0" {
		t.Fatalf("model %q", rec.RequestedModel)
	}
	res, body = call("/bedrock/model/x/apply-guardrail")
	if res.StatusCode != 404 || res.Header.Get("X-Amzn-Errortype") != "ResourceNotFoundException" {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}
