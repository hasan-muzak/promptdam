package livecheck_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/auth/bearer"
	"google.golang.org/genai"

	"github.com/hasan-muzak/keel-core/pkg/events"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

// maxDoorSpend stops TestDoors: a cent or two is what it costs.
const maxDoorSpend = 0.10 // dollars

// TestDoors calls a gateway on the owner's keys (PROMPTDAM_TEST_ANTHROPIC_KEY,
// PROMPTDAM_TEST_OPENROUTER_KEY) with Google's genai SDK and AWS's
// bedrockruntime SDK, as their users would after changing the base URL:
// Gemini (through OpenRouter) and Claude through each door, streamed and
// not, a tool call's round trip, a router choosing between them, and
// token counts. Then the request log, by door.
func TestDoors(t *testing.T) {
	anthropicKey, openRouterKey := os.Getenv("PROMPTDAM_TEST_ANTHROPIC_KEY"), os.Getenv("PROMPTDAM_TEST_OPENROUTER_KEY")
	if anthropicKey == "" || openRouterKey == "" {
		t.Skip("PROMPTDAM_TEST_ANTHROPIC_KEY and PROMPTDAM_TEST_OPENROUTER_KEY (.env) are needed")
	}
	ctx := context.Background()
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	w.Grant(catalog.Routers, catalog.RoutingLearned, catalog.PromptCaching)
	w.Serves("main", "fake-model") // the World's fake provider answers nothing here
	if err := w.App.Prices.Sync(ctx, true); err != nil {
		t.Fatalf("syncing prices: %v", err)
	}
	for name, in := range map[string]providers.Input{
		"anthropic":  {Name: "anthropic", Kind: "anthropic", APIKey: &anthropicKey, Enabled: true},
		"openrouter": {Name: "openrouter", Kind: "openrouter", APIKey: &openRouterKey, Enabled: true},
	} {
		if _, err := w.App.Providers.Create(w.Scoped(), w.Org, w.Project, in); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	const claude, gemini = "claude-haiku-5-5", "google/gemini-3.8-flash"
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "advisor", Status: tiers.Active,
		DecisionModel: "anthropic/" + claude,
		Spec: tiers.Spec{Tiers: []tiers.Tier{
			{Name: "fast", Model: "openrouter/" + gemini, Description: "Definitions, single facts, rewording and short answers.",
				Examples: []string{"What does CIF stand for?"}},
			{Name: "expert", Model: "anthropic/" + claude, Description: "Legal analysis, calculations and comparisons across laws.",
				Examples: []string{"Compare the customs value under CIF and FOB for this invoice."}},
		}, Default: "expert"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool { // the providers reached the gateway
		status, _, _ := w.Call("/v1/messages", `{"model":"`+claude+`","max_tokens":5,"messages":[{"role":"user","content":"Say ok."}]}`)
		return status == 200
	})

	// Google's SDK, its base URL the gateway's door.
	g, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: w.Raw, Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: w.Gateway.URL + "/gemini/"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &genai.GenerateContentConfig{MaxOutputTokens: 600,
		SystemInstruction: genai.NewContentFromText("You help customs brokers. Answer in one or two sentences.", genai.RoleUser)}
	question := genai.Text("What is a CMR consignment note?")
	for _, model := range []string{gemini, claude} {
		resp, err := g.Models.GenerateContent(ctx, model, question, cfg)
		if err != nil {
			t.Errorf("genai, %s: %v", model, err)
			continue
		}
		t.Logf("genai, %s: %q (%s; prompt %d, cached %d, answer %d, thinking %d tokens)", model, clip(resp.Text(), 120),
			resp.Candidates[0].FinishReason, resp.UsageMetadata.PromptTokenCount, resp.UsageMetadata.CachedContentTokenCount,
			resp.UsageMetadata.CandidatesTokenCount, resp.UsageMetadata.ThoughtsTokenCount)
		var text strings.Builder
		for chunk, err := range g.Models.GenerateContentStream(ctx, model, question, cfg) {
			if err != nil {
				t.Errorf("genai, %s, streamed: %v", model, err)
				break
			}
			text.WriteString(chunk.Text())
		}
		t.Logf("genai, %s, streamed: %q", model, clip(text.String(), 120))
		// Google counts for Gemini on its own API or Vertex; through
		// OpenRouter, Gemini can't be counted.
		if n, err := g.Models.CountTokens(ctx, model, question, nil); err != nil {
			t.Logf("genai, %s, count: %v", model, err)
		} else {
			t.Logf("genai, %s, count: %d tokens", model, n.TotalTokens)
		}
	}

	// A tool call's round trip, each model calling and reading the result.
	rate := &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "duty_rate",
		Description: "The customs duty rate of an HS code in Turkey",
		Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"hs_code": {Type: genai.TypeString}},
			Required: []string{"hs_code"}}}}}
	toolCfg := &genai.GenerateContentConfig{MaxOutputTokens: 800, Tools: []*genai.Tool{rate},
		ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAuto}}}
	ask := genai.Text("Use the tool: what is the duty rate of HS code 8471.30 (laptops)?")
	for _, model := range []string{gemini, claude} {
		resp, err := g.Models.GenerateContent(ctx, model, ask, toolCfg)
		if err != nil || len(resp.FunctionCalls()) == 0 {
			t.Errorf("genai tools, %s: no call (%v)", model, err)
			continue
		}
		call := resp.FunctionCalls()[0]
		reply := genai.NewPartFromFunctionResponse(call.Name, map[string]any{"rate": "0%", "note": "ITA products"})
		reply.FunctionResponse.ID = call.ID
		history := append(ask, resp.Candidates[0].Content, genai.NewContentFromParts([]*genai.Part{reply}, genai.RoleUser))
		final, err := g.Models.GenerateContent(ctx, model, history, toolCfg)
		if err != nil {
			t.Errorf("genai tools, %s, the result: %v", model, err)
			continue
		}
		signed := false
		for _, p := range resp.Candidates[0].Content.Parts {
			signed = signed || p.FunctionCall != nil && len(p.ThoughtSignature) > 0
		}
		t.Logf("genai tools, %s: called %s(%v), signed %v, then %q", model, call.Name, call.Args, signed, clip(final.Text(), 120))
	}

	// The router, through Google's SDK.
	for _, q := range []string{"What does CIF stand for?", "Compare the customs value under CIF and FOB for a 10,000 EUR invoice with 800 EUR freight."} {
		resp, err := g.Models.GenerateContent(ctx, "advisor", genai.Text(q), &genai.GenerateContentConfig{MaxOutputTokens: 2000,
			SystemInstruction: cfg.SystemInstruction})
		if err != nil {
			t.Errorf("genai, advisor: %v", err)
			continue
		}
		t.Logf("genai, advisor, %q: %q (%s)", q, clip(resp.Text(), 100), resp.Candidates[0].FinishReason)
	}

	// AWS's SDK, its endpoint the door, the Promptdam key a Bedrock API key
	// (over HTTPS, as AWS's Go SDK sends bearer tokens).
	tls := httptest.NewTLSServer(w.Gateway.Config.Handler)
	t.Cleanup(tls.Close)
	b := bedrockruntime.New(bedrockruntime.Options{Region: "eu-central-1", BaseEndpoint: aws.String(tls.URL + "/bedrock"),
		BearerAuthTokenProvider: bearer.StaticTokenProvider{Token: bearer.Token{Value: w.Raw}},
		AuthSchemePreference:    []string{"httpBearerAuth"}, HTTPClient: tls.Client()})
	msgs := []types.Message{{Role: types.ConversationRoleUser, Content: []types.ContentBlock{
		&types.ContentBlockMemberText{Value: "What is a CMR consignment note? One sentence."}}}}
	for _, model := range []string{claude, gemini} {
		out, err := b.Converse(ctx, &bedrockruntime.ConverseInput{ModelId: aws.String(model), Messages: msgs,
			InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(600)}})
		if err != nil {
			t.Errorf("bedrock, %s: %v", model, err)
			continue
		}
		text := out.Output.(*types.ConverseOutputMemberMessage).Value.Content[0].(*types.ContentBlockMemberText).Value
		t.Logf("bedrock converse, %s: %q (input %d, output %d tokens)", model, clip(text, 120),
			aws.ToInt32(out.Usage.InputTokens), aws.ToInt32(out.Usage.OutputTokens))
		stream, err := b.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{ModelId: aws.String(model), Messages: msgs,
			InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(600)}})
		if err != nil {
			t.Errorf("bedrock stream, %s: %v", model, err)
			continue
		}
		var streamed strings.Builder
		es := stream.GetStream()
		for ev := range es.Events() {
			if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
				if txt, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
					streamed.WriteString(txt.Value)
				}
			}
		}
		_ = es.Close()
		if es.Err() != nil {
			t.Errorf("bedrock stream, %s: %v", model, es.Err())
		}
		t.Logf("bedrock converse, %s, streamed: %q", model, clip(streamed.String(), 120))
	}
	inv, err := b.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{ModelId: aws.String(claude), ContentType: aws.String("application/json"),
		Body: []byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":60,"messages":[{"role":"user","content":"Say ok."}]}`)})
	if err != nil {
		t.Errorf("bedrock invoke: %v", err)
	} else {
		t.Logf("bedrock invoke, %s: %s", claude, clip(string(inv.Body), 160))
	}
	n, err := b.CountTokens(ctx, &bedrockruntime.CountTokensInput{ModelId: aws.String(claude),
		Input: &types.CountTokensInputMemberConverse{Value: types.ConverseTokensRequest{Messages: msgs}}})
	if err != nil {
		t.Errorf("bedrock count: %v", err)
	} else {
		t.Logf("bedrock count, %s: %d tokens", claude, aws.ToInt32(n.InputTokens))
	}

	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Door   string `ch:"door"`
		Served string `ch:"served_model"`
		Tier   string `ch:"tier"`
		Source string `ch:"tier_source"`
		Decide string `ch:"decision"`
		Status uint16 `ch:"status"`
		Input  uint32 `ch:"input_tokens"`
		Read   uint32 `ch:"cache_read_tokens"`
		Output uint32 `ch:"output_tokens"`
		Cost   int64  `ch:"cost_micros"`
		Error  string `ch:"error_code"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT door, served_model, tier, tier_source, decision, status, input_tokens,
		cache_read_tokens, output_tokens, cost_micros, error_code FROM requests WHERE {tenant} ORDER BY ts`); err != nil {
		t.Fatal(err)
	}
	var spent float64
	out := &strings.Builder{}
	fmt.Fprintf(out, "\n| Door | Model | Tier (why) | Status | Input | Read from cache | Output | Cost |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		spent += usd(r.Cost)
		tier := r.Tier
		if tier != "" {
			tier += " (" + r.Source + ", " + r.Decide + ")"
		}
		fmt.Fprintf(out, "| %s | %s %s | %s | %d | %d | %d | %d | $%.5f |\n", r.Door, r.Served, r.Error, tier, r.Status, r.Input,
			r.Read, r.Output, usd(r.Cost))
	}
	fmt.Fprintf(out, "\nSpent $%.4f in %d calls.\n", spent, len(rows))
	t.Log(out.String())
	if spent > maxDoorSpend {
		t.Fatalf("spent $%.4f, past $%.2f", spent, maxDoorSpend)
	}
}
