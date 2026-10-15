package gemini_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/gemini"
	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// sameJSON fails unless got and want hold the same JSON.
func sameJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// The SDKs' paths, Vertex's included, name the operation and the model;
// the method follows the last colon, so a router's tier stays in the
// model.
func TestParsePath(t *testing.T) {
	for path, want := range map[string]gemini.Call{
		"v1beta/models/gemini-3.5-flash:generateContent":                             {Op: gemini.OpGenerate, Model: "gemini-3.5-flash"},
		"/v1/models/gemini-3.5-flash:streamGenerateContent":                          {Op: gemini.OpStream, Model: "gemini-3.5-flash"},
		"v1beta/models/advisor:fast:generateContent":                                 {Op: gemini.OpGenerate, Model: "advisor:fast"},
		"v1beta/models/deepseek/deepseek-v4.1-flash:generateContent":                 {Op: gemini.OpGenerate, Model: "deepseek/deepseek-v4.1-flash"},
		"v1beta1/publishers/google/models/gemini-3.5-pro:countTokens":                {Op: gemini.OpCount, Model: "gemini-3.5-pro"},
		"v1beta1/projects/acme/locations/eu/publishers/google/models/g:embedContent": {Op: gemini.OpEmbed, Model: "g"},
		"v1beta/models/text-embedding-005:batchEmbedContents":                        {Op: gemini.OpBatchEmbed, Model: "text-embedding-005"},
		"v1beta/models": {Op: gemini.OpList},
		"v1beta1/publishers/meta/models/llama-4-maverick-17b-128e-instruct-maas:generateContent": {Op: gemini.OpGenerate,
			Model: "meta/llama-4-maverick-17b-128e-instruct-maas"},
		"v1beta1/publishers//models/x:generateContent":                                {},
		"v1beta/models/gemini-3.5-flash":                                              {Op: gemini.OpGet, Model: "gemini-3.5-flash"},
		"v1alpha/publishers/google/models/gemini-3.5-flash:streamGenerateContent":     {Op: gemini.OpStream, Model: "gemini-3.5-flash"},
		"v1beta1/projects/acme/locations/us-central1/publishers/google/models/x:nope": {},
		"v2/models/x:generateContent":                                                 {},
		"v1beta/tunedModels/mine:generateContent":                                     {},
		"v1beta/models/:generateContent":                                              {},
		"v1beta/projects/acme/models/x:generateContent":                               {},
	} {
		got, ok := gemini.ParsePath(path)
		if ok != (want.Op != 0) || got != want {
			t.Errorf("%s: got %+v (%v), want %+v", path, got, ok, want)
		}
	}
}

// A generateContent request becomes Chat's: the system instruction, user
// parts (images and PDFs inline), the model's function calls with ids made
// up per turn and their thought signatures, the responses matched to them
// by name, tools with Gemini's schemas as JSON Schema, the tool choice,
// sampling, the answer's format and the thinking effort. Snake_case
// fields read as camelCase ones; what has no equivalent is listed.
func TestRequest(t *testing.T) {
	in := `{
		"system_instruction":{"parts":[{"text":"You are a customs assistant."},{"text":"Be brief."}]},
		"contents":[
			{"role":"user","parts":[{"text":"What is this form?"},
				{"inlineData":{"mimeType":"image/png","data":"iVBOR"}},
				{"inline_data":{"mime_type":"application/pdf","data":"JVBER"}}]},
			{"role":"model","parts":[{"text":"Let me check."},
				{"functionCall":{"name":"lookup","args":{"code":"T1"}},"thoughtSignature":"c2ln"},
				{"functionCall":{"name":"lookup","args":{"code":"T2"}}},
				{"text":"","thoughtSignature":"bGFzdA=="}]},
			{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"form":"transit"}}},
				{"functionResponse":{"name":"lookup","response":{"form":"transit, second"}}},
				{"text":"Thanks"}]},
			{"role":"model","parts":[{"text":"thinking...","thought":true},{"text":"Both are transit declarations."}]}
		],
		"tools":[{"functionDeclarations":[
			{"name":"lookup","description":"Look a form up","parameters":{"type":"OBJECT",
				"properties":{"code":{"type":"STRING","nullable":true},"tags":{"type":"ARRAY","items":{"type":"STRING"},"max_items":"3"}},
				"required":["code"],"propertyOrdering":["code","tags"]}},
			{"name":"rate","parametersJsonSchema":{"type":"object","properties":{"hs":{"type":"string"}}}}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"]}},
		"generationConfig":{"temperature":0.3,"topP":0.9,"topK":40,"maxOutputTokens":800,"stopSequences":["###"],
			"seed":7,"responseMimeType":"application/json",
			"responseSchema":{"type":"OBJECT","properties":{"answer":{"type":"STRING"}}},
			"thinkingConfig":{"thinkingBudget":4096,"includeThoughts":true}},
		"safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_NONE"}],
		"labels":{"team":"customs"}
	}`
	out, dropped, err := gemini.Request([]byte(in), "gemini-3.5-flash", true)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"model":"gemini-3.5-flash","stream":true,"stream_options":{"include_usage":true},
		"messages":[
			{"role":"system","content":"You are a customs assistant.\n\nBe brief."},
			{"role":"user","content":[{"type":"text","text":"What is this form?"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}},
				{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBER","filename":"document.pdf"}}]},
			{"role":"assistant","content":"Let me check.","tool_calls":[
				{"id":"call_1_0","type":"function","function":{"name":"lookup","arguments":"{\"code\":\"T1\"}"},
					"extra_content":{"google":{"thought_signature":"c2ln"}}},
				{"id":"call_1_1","type":"function","function":{"name":"lookup","arguments":"{\"code\":\"T2\"}"}}]},
			{"role":"tool","tool_call_id":"call_1_0","content":"{\"form\":\"transit\"}"},
			{"role":"tool","tool_call_id":"call_1_1","content":"{\"form\":\"transit, second\"}"},
			{"role":"user","content":"Thanks"},
			{"role":"assistant","content":"Both are transit declarations."}
		],
		"tools":[{"type":"function","function":{"name":"lookup","description":"Look a form up",
			"parameters":{"type":"object","properties":{"code":{"type":["string","null"]},
				"tags":{"type":"array","items":{"type":"string"},"maxItems":3}},"required":["code"]}}}],
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"temperature":0.3,"top_p":0.9,"max_tokens":800,"stop":["###"],"seed":7,
		"response_format":{"type":"json_schema","json_schema":{"name":"response",
			"schema":{"type":"object","properties":{"answer":{"type":"string"}}}}},
		"reasoning_effort":"medium"}`)
	want := "generationConfig.thinkingConfig.includeThoughts,generationConfig.topK,labels,safetySettings"
	if strings.Join(dropped, ",") != want {
		t.Fatalf("dropped %v, want %s", dropped, want)
	}
}

// Thinking levels and budgets become efforts; a response with an id
// answers its call; a function response with no call before it gets an id
// of its own; NONE forbids the tools.
func TestRequestDetails(t *testing.T) {
	in := `{"contents":[
			{"role":"model","parts":[{"functionCall":{"id":"fc-9","name":"a","args":{}}}]},
			{"role":"user","parts":[{"functionResponse":{"id":"fc-9","name":"a","response":{"ok":true}}},
				{"functionResponse":{"name":"b"}}]}],
		"tools":[{"functionDeclarations":[{"name":"a"}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"NONE"}},
		"generationConfig":{"thinkingConfig":{"thinkingLevel":"LOW"},"candidateCount":2}}`
	out, _, err := gemini.Request([]byte(in), "m", false)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"model":"m","messages":[
			{"role":"assistant","tool_calls":[{"id":"fc-9","type":"function","function":{"name":"a","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"fc-9","content":"{\"ok\":true}"},
			{"role":"tool","tool_call_id":"call_b","content":"{}"}],
		"tools":[{"type":"function","function":{"name":"a"}}],"tool_choice":"none","n":2,"reasoning_effort":"low"}`)

	for budget, effort := range map[string]string{"0": "none", "-1": "", "512": "low", "20000": "high"} {
		out, _, err := gemini.Request([]byte(`{"contents":[{"parts":[{"text":"hi"}]}],
			"generationConfig":{"thinkingConfig":{"thinkingBudget":`+budget+`}}}`), "m", false)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		_ = json.Unmarshal(out, &got)
		if got.ReasoningEffort != effort {
			t.Errorf("budget %s: effort %q, want %q", budget, got.ReasoningEffort, effort)
		}
	}
}

// What Chat can't carry is refused, naming it: Google's server tools, its
// context cache and uploaded files, video, enum answers, answers in other
// modalities, stricter safety settings.
func TestRequestRefuses(t *testing.T) {
	user := func(part string) string { return `{"contents":[{"role":"user","parts":[` + part + `]}]` }
	for name, body := range map[string]string{
		"search":   user(`{"text":"news?"}`) + `,"tools":[{"googleSearch":{}}]}`,
		"code":     user(`{"text":"run"}`) + `,"tools":[{"codeExecution":{}}]}`,
		"cache":    user(`{"text":"hi"}`) + `,"cachedContent":"cachedContents/abc"}`,
		"file api": user(`{"fileData":{"mimeType":"application/pdf","fileUri":"https://generativelanguage.googleapis.com/v1beta/files/x"}}`) + `}`,
		"gcs":      user(`{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/a.png"}}`) + `}`,
		"video":    user(`{"inlineData":{"mimeType":"video/mp4","data":"AAAA"}}`) + `}`,
		"enum":     user(`{"text":"pick"}`) + `,"generationConfig":{"responseMimeType":"text/x.enum"}}`,
		"image":    user(`{"text":"draw"}`) + `,"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`,
		"strict":   user(`{"text":"hi"}`) + `,"safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_LOW_AND_ABOVE"}]}`,
		"executed": `{"contents":[{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"1"}}]}]}`,
		"empty":    `{"contents":[]}`,
		"files":    user(`{"functionResponse":{"name":"a","response":{},"parts":[{"inlineData":{"mimeType":"image/png","data":"x"}}]}}`) + `}`,
	} {
		_, _, err := gemini.Request([]byte(body), "m", false)
		var te *translate.Error
		if !errors.As(err, &te) {
			t.Errorf("%s: err %v, want a refusal", name, err)
		}
	}
	// An image by https URL, WAV audio and an inline text file are taken.
	out, _, err := gemini.Request([]byte(user(`{"fileData":{"mimeType":"image/jpeg","fileUri":"https://example.com/a.jpg"}},
		{"inlineData":{"mimeType":"audio/wav","data":"UklG"}},{"inlineData":{"mimeType":"text/csv","data":"YSxi"}}`)+`}`), "m", false)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"model":"m","messages":[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"https://example.com/a.jpg"}},
		{"type":"input_audio","input_audio":{"data":"UklG","format":"wav"}},
		{"type":"text","text":"a,b"}]}]}`)
}

// A Chat answer becomes Gemini's: text and function calls (thought
// signatures and ids kept, broken arguments kept as a string), the finish
// reason, and the usage with the thinking and the cache counted apart.
func TestResponse(t *testing.T) {
	in := `{"id":"chatcmpl-1","model":"gemini-3.5-flash","choices":[{"index":0,"finish_reason":"tool_calls",
		"message":{"role":"assistant","content":"Checking.","tool_calls":[
			{"id":"call_a","type":"function","function":{"name":"lookup","arguments":"{\"code\":\"T1\"}"},
				"extra_content":{"google":{"thought_signature":"c2ln"}}},
			{"id":"call_b","type":"function","function":{"name":"lookup","arguments":"oops"}}]}}],
		"usage":{"prompt_tokens":100,"completion_tokens":30,"total_tokens":130,
			"prompt_tokens_details":{"cached_tokens":60},"completion_tokens_details":{"reasoning_tokens":10}}}`
	out, err := gemini.Response([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[
			{"text":"Checking."},
			{"functionCall":{"id":"call_a","name":"lookup","args":{"code":"T1"}},"thoughtSignature":"c2ln"},
			{"functionCall":{"id":"call_b","name":"lookup","args":{"arguments":"oops"}}}]}}],
		"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":10,
			"cachedContentTokenCount":60,"totalTokenCount":130},
		"modelVersion":"gemini-3.5-flash","responseId":"chatcmpl-1"}`)

	out, err = gemini.Response([]byte(`{"id":"x","model":"m","choices":[{"index":0,"finish_reason":"length",
		"message":{"content":null}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"candidates":[{"index":0,"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":""}]}}],
		"modelVersion":"m","responseId":"x"}`)
}

// chatStream is a Chat stream: text, a function call in pieces, its finish
// reason, then the usage on its own.
var chatStream = []string{
	`{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
	`{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
	`{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a",
		"function":{"name":"lookup","arguments":"{\"co"},"extra_content":{"google":{"thought_signature":"c2ln"}}}]}}]}`,
	`{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"de\":1}"}}]}}]}`,
	`{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	`{"id":"c1","model":"gemini-3.5-flash","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`,
	`[DONE]`,
}

const (
	chunkHel  = `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]},"index":0}],"modelVersion":"gemini-3.5-flash","responseId":"c1"}`
	chunkLo   = `{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"index":0}],"modelVersion":"gemini-3.5-flash","responseId":"c1"}`
	chunkLast = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"lookup","args":{"code":1}},` +
		`"thoughtSignature":"c2ln"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":12,` +
		`"candidatesTokenCount":5,"totalTokenCount":17},"modelVersion":"gemini-3.5-flash","responseId":"c1"}`
)

func run(s *gemini.Stream, events []string) string {
	var out []byte
	for _, e := range events {
		out = s.Event(out, []byte(e))
	}
	return string(s.End(out))
}

// A Chat stream becomes Gemini's: server-sent events, or the JSON array
// Google streams without alt=sse; the function call goes out whole on the
// last chunk, with the finish reason and the usage.
func TestStream(t *testing.T) {
	want := "data: " + chunkHel + "\n\n" + "data: " + chunkLo + "\n\n" + "data: " + chunkLast + "\n\n"
	if got := run(gemini.NewStream(true), chatStream); got != want {
		t.Fatalf("sse:\n%s\nwant\n%s", got, want)
	}
	want = "[" + chunkHel + ",\r\n" + chunkLo + ",\r\n" + chunkLast + "]"
	if got := run(gemini.NewStream(false), chatStream); got != want {
		t.Fatalf("array:\n%s\nwant\n%s", got, want)
	}
	if got := run(gemini.NewStream(false), nil); got != "[]" {
		t.Fatalf("empty array %q", got)
	}
	// A stream without its usage still ends, at End.
	got := run(gemini.NewStream(true), []string{chatStream[0],
		`{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`})
	if !strings.HasSuffix(got, `"finishReason":"MAX_TOKENS","index":0}],"modelVersion":"gemini-3.5-flash","responseId":"c1"}`+"\n\n") {
		t.Fatalf("no usage: %s", got)
	}
	// An error mid-stream ends it in Google's shape.
	got = run(gemini.NewStream(true), []string{chatStream[0],
		`{"error":{"message":"Overloaded","type":"overloaded_error"}}`, chatStream[1]})
	if !strings.HasSuffix(got, `data: {"error":{"code":503,"message":"Overloaded","status":"UNAVAILABLE"}}`+"\n\n") {
		t.Fatalf("error: %s", got)
	}
}

// Errors take Google's shape, with its status names; Anthropic's 529 is
// Google's 503.
func TestErrorBody(t *testing.T) {
	sameJSON(t, gemini.ErrorBody(400, []byte(`{"error":{"message":"bad","type":"invalid_request_error"}}`)),
		`{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`)
	sameJSON(t, gemini.ErrorBody(429, []byte(`[{"error":{"code":429,"message":"slow down"}}]`)),
		`{"error":{"code":429,"message":"slow down","status":"RESOURCE_EXHAUSTED"}}`)
	sameJSON(t, gemini.ErrorBody(529, []byte(`not json`)),
		`{"error":{"code":503,"message":"the gateway answered ","status":"UNAVAILABLE"}}`)
	sameJSON(t, gemini.ErrorBody(401, []byte(`{"error":{"message":"invalid key"}}`)),
		`{"error":{"code":401,"message":"invalid key","status":"UNAUTHENTICATED"}}`)
}

// embedContent and batchEmbedContents become OpenAI embeddings requests
// and back; different dimensions in one batch are refused.
func TestEmbed(t *testing.T) {
	out, dropped, err := gemini.EmbedRequest([]byte(`{"content":{"parts":[{"text":"a"},{"text":"b"}]},
		"taskType":"RETRIEVAL_QUERY","outputDimensionality":256}`), "text-embedding-005", false)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"model":"text-embedding-005","input":"a\nb","encoding_format":"float","dimensions":256}`)
	if strings.Join(dropped, ",") != "taskType" {
		t.Fatalf("dropped %v", dropped)
	}
	out, _, err = gemini.EmbedRequest([]byte(`{"requests":[{"model":"models/e","content":{"parts":[{"text":"a"}]}},
		{"model":"models/e","content":{"parts":[{"text":"b"}]}}]}`), "e", true)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"model":"e","input":["a","b"],"encoding_format":"float"}`)
	if _, _, err := gemini.EmbedRequest([]byte(`{"requests":[{"content":{"parts":[{"text":"a"}]},"outputDimensionality":8},
		{"content":{"parts":[{"text":"b"}]}}]}`), "e", true); err == nil {
		t.Fatal("different dimensions taken")
	}

	answer := []byte(`{"object":"list","data":[{"index":1,"embedding":[0.3]},{"index":0,"embedding":[0.1,0.2]}]}`)
	out, err = gemini.EmbedResponse(answer, true)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3]}]}`)
	out, err = gemini.EmbedResponse(answer, false)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"embedding":{"values":[0.1,0.2]}}`)
}

// The model list takes Gemini's shape.
func TestModels(t *testing.T) {
	sameJSON(t, gemini.Models([]string{"gemini-3.5-flash"}), `{"models":[{"name":"models/gemini-3.5-flash",
		"baseModelId":"gemini-3.5-flash","version":"001","displayName":"gemini-3.5-flash",
		"supportedGenerationMethods":["generateContent","streamGenerateContent","countTokens","embedContent"]}]}`)
}

// countTokens' request is generateContent's, or wraps one.
func TestCountRequest(t *testing.T) {
	wrapped := `{"generateContentRequest":{"model":"models/m","contents":[{"parts":[{"text":"hi"}]}]}}`
	out, err := gemini.CountRequest([]byte(wrapped))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"model":"models/m","contents":[{"parts":[{"text":"hi"}]}]}`)
	plain := `{"contents":[{"parts":[{"text":"hi"}]}]}`
	if out, err = gemini.CountRequest([]byte(plain)); err != nil || string(out) != plain {
		t.Fatalf("%s %v", out, err)
	}
	sameJSON(t, gemini.CountResponse(42), `{"totalTokens":42}`)
}
