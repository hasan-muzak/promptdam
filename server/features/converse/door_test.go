package converse_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	"github.com/hasan-muzak/promptdam/server/features/converse"
	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// Runtime paths name the model and the operation, the id escaped as AWS's
// SDKs send it (':' and an ARN's '/') or as Anthropic's do (':' plain).
func TestParsePath(t *testing.T) {
	type want struct {
		model string
		op    converse.Op
	}
	for path, w := range map[string]want{
		"/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse": {"us.anthropic.claude-sonnet-4-5-20250929-v1:0", converse.OpConverse},
		"/model/amazon.nova-lite-v1:0/converse-stream":                   {"amazon.nova-lite-v1:0", converse.OpConverseStream},
		"//model/claude-haiku-4-5/invoke":                                {"claude-haiku-4-5", converse.OpInvoke},
		"/model/arn%3Aaws%3Abedrock%3Aus-east-1%3A123%3Ainference-profile%2Fus.amazon.nova-pro-v1%3A0/invoke-with-response-stream": {
			"arn:aws:bedrock:us-east-1:123:inference-profile/us.amazon.nova-pro-v1:0", converse.OpInvokeStream},
		"/model/advisor%3Afast/count-tokens": {"advisor:fast", converse.OpCountTokens},
		"/model/x/converse-something":        {},
		"/models/x/converse":                 {},
		"/model//converse":                   {},
		"/model/converse":                    {},
	} {
		model, op, ok := converse.ParsePath(path)
		if ok != (w.op != 0) || model != w.model || op != w.op {
			t.Errorf("%s: got %q %v %v, want %+v", path, model, op, ok, w)
		}
	}
}

// A Converse request becomes Messages: system and messages with their
// cache points as breakpoints on the block before, images, PDF and text
// documents, tool uses and results (JSON as text), reasoning, quoted
// citations as text, the sampling, tools and choice, Claude's fields
// from additionalModelRequestFields, the JSON schema, the user. What has
// no equivalent is listed.
func TestFromConverse(t *testing.T) {
	in := `{
		"system":[{"text":"You are a customs assistant."},{"cachePoint":{"type":"default","ttl":"1h"}}],
		"messages":[
			{"role":"user","content":[{"text":"Read these."},
				{"image":{"format":"png","source":{"bytes":"iVBOR"}}},
				{"document":{"format":"pdf","name":"invoice","source":{"bytes":"JVBER"}}},
				{"document":{"format":"csv","name":"lines","source":{"bytes":"YSxi"}}},
				{"cachePoint":{"type":"default"}}]},
			{"role":"assistant","content":[
				{"reasoningContent":{"reasoningText":{"text":"Check the codes.","signature":"sig"}}},
				{"citationsContent":{"content":[{"text":"Quoted."}]}},
				{"toolUse":{"toolUseId":"t1","name":"lookup","input":{"hs":"8471"}}}]},
			{"role":"user","content":[{"toolResult":{"toolUseId":"t1","status":"error",
				"content":[{"json":{"rate": 0.02}},{"text":"stale"}]}}]}
		],
		"inferenceConfig":{"maxTokens":500,"temperature":0.2,"stopSequences":["###"]},
		"toolConfig":{"tools":[{"toolSpec":{"name":"lookup","description":"Duty rates",
			"inputSchema":{"json":{"type":"object","properties":{"hs":{"type":"string"}}}},"strict":true}},
			{"cachePoint":{"type":"default"}}],"toolChoice":{"any":{}}},
		"additionalModelRequestFields":{"top_k":20,"thinking":{"type":"adaptive"},"anthropic_beta":["context-1m"],"foo":1},
		"outputConfig":{"textFormat":{"type":"json_schema","structure":{"jsonSchema":{"schema":"{\"type\":\"object\"}"}}}},
		"requestMetadata":{"user_id":"broker-7","team":"ops"},
		"performanceConfig":{"latency":"optimized"}
	}`
	c, err := converse.FromConverse([]byte(in), "claude-sonnet-5-5", true)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, c.Body, `{"model":"claude-sonnet-5-5","max_tokens":500,"stream":true,
		"system":[{"type":"text","text":"You are a customs assistant.","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Read these."},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}},
				{"type":"document","title":"invoice","source":{"type":"base64","media_type":"application/pdf","data":"JVBER"}},
				{"type":"document","title":"lines","source":{"type":"text","media_type":"text/plain","data":"a,b"},
					"cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"thinking","thinking":"Check the codes.","signature":"sig"},
				{"type":"text","text":"Quoted."},
				{"type":"tool_use","id":"t1","name":"lookup","input":{"hs":"8471"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,
				"content":[{"type":"text","text":"{\"rate\":0.02}"},{"type":"text","text":"stale"}]}]}
		],
		"temperature":0.2,"stop_sequences":["###"],
		"tools":[{"name":"lookup","description":"Duty rates","input_schema":{"type":"object","properties":{"hs":{"type":"string"}}},
			"strict":true,"cache_control":{"type":"ephemeral"}}],
		"tool_choice":{"type":"any"},"top_k":20,"thinking":{"type":"adaptive"},
		"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}},
		"metadata":{"user_id":"broker-7"}}`)
	if strings.Join(c.Betas, ",") != "context-1m" || c.NoMaxTokens {
		t.Fatalf("betas %v, no max %v", c.Betas, c.NoMaxTokens)
	}
	if got := strings.Join(c.Dropped, ","); got != "additionalModelRequestFields.foo,performanceConfig,requestMetadata" {
		t.Fatalf("dropped %s", got)
	}

	// Without maxTokens, the model's own limit is wanted.
	c, err = converse.FromConverse([]byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`), "amazon.nova-lite-v1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, c.Body, `{"model":"amazon.nova-lite-v1:0","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if !c.NoMaxTokens {
		t.Fatal("no limit not reported")
	}
}

// A request Messages translates to Converse comes back as it was.
func TestConverseRoundTrip(t *testing.T) {
	in := `{"model":"claude-sonnet-5-5","max_tokens":300,"system":[{"type":"text","text":"Be brief."}],
		"messages":[{"role":"user","content":[{"type":"text","text":"list them"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ls","input":{"dir":"src"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a.go"}]}]}],
		"tools":[{"name":"ls","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"ls"},"temperature":0.5}`
	cv, _, err := converse.Request([]byte(in), "anthropic.claude-sonnet-5-5")
	if err != nil {
		t.Fatal(err)
	}
	back, err := converse.FromConverse(cv, "claude-sonnet-5-5", false)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, back.Body, in)
}

// What Messages can't carry is refused: guardrails, S3, video, Office
// documents, Bedrock's system tools and prompt management.
func TestFromConverseRefuses(t *testing.T) {
	msg := func(block string) string { return `{"messages":[{"role":"user","content":[` + block + `]}]` }
	for name, body := range map[string]string{
		"guardrail":    msg(`{"text":"hi"}`) + `,"guardrailConfig":{"guardrailIdentifier":"g","guardrailVersion":"1"}}`,
		"guard block":  msg(`{"guardContent":{"text":{"text":"hi"}}}`) + `}`,
		"s3":           msg(`{"image":{"format":"png","source":{"s3Location":{"uri":"s3://b/a.png"}}}}`) + `}`,
		"video":        msg(`{"video":{"format":"mp4","source":{"bytes":"AAAA"}}}`) + `}`,
		"docx":         msg(`{"document":{"format":"docx","name":"a","source":{"bytes":"AAAA"}}}`) + `}`,
		"system tool":  msg(`{"text":"hi"}`) + `,"toolConfig":{"tools":[{"systemTool":{"name":"nova_grounding"}}]}}`,
		"prompt":       msg(`{"text":"hi"}`) + `,"promptVariables":{"x":{"text":"y"}}}`,
		"not json":     `[`,
		"system image": `{"system":[{"guardContent":{"text":{"text":"x"}}}],"messages":[]}`,
	} {
		_, err := converse.FromConverse([]byte(body), "m", false)
		var te *translate.Error
		if !errors.As(err, &te) {
			t.Errorf("%s: err %v, want a refusal", name, err)
		}
	}
}

// InvokeModel's Claude body is Messages: anthropic_version goes, the
// betas move to the header, the model and the stream come from the path.
// Other models' bodies are refused.
func TestFromInvoke(t *testing.T) {
	c, err := converse.FromInvoke([]byte(`{"anthropic_version":"bedrock-2023-05-31","anthropic_beta":["a","b"],
		"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`), "claude-haiku-4-5", true)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, c.Body, `{"max_tokens":64,"messages":[{"role":"user","content":"hi"}],"model":"claude-haiku-4-5","stream":true}`)
	if strings.Join(c.Betas, ",") != "a,b" || c.NoMaxTokens {
		t.Fatalf("betas %v, no max %v", c.Betas, c.NoMaxTokens)
	}
	_, err = converse.FromInvoke([]byte(`{"prompt":"<|begin_of_text|>hi","max_gen_len":64}`), "meta.llama3-3-70b-instruct-v1:0", false)
	var te *translate.Error
	if !errors.As(err, &te) {
		t.Fatalf("a Llama body: %v", err)
	}
}

// A Messages answer becomes Converse's, with its usage (the cache's reads
// and writes, by lifetime) and the call's latency.
func TestToConverse(t *testing.T) {
	out, err := converse.ToConverse([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5",
		"content":[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"Rate is 2%."},
			{"type":"tool_use","id":"t1","name":"lookup","input":{"hs":"8471"}},{"type":"server_tool_use","id":"x","name":"web_search"}],
		"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":300,
			"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":40}}}`),
		1500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"output":{"message":{"role":"assistant","content":[
			{"reasoningContent":{"reasoningText":{"text":"hmm","signature":"s"}}},{"text":"Rate is 2%."},
			{"toolUse":{"toolUseId":"t1","name":"lookup","input":{"hs":"8471"}}}]}},
		"stopReason":"tool_use",
		"usage":{"inputTokens":10,"outputTokens":20,"totalTokens":370,"cacheReadInputTokens":300,"cacheWriteInputTokens":40,
			"cacheDetails":[{"ttl":"1h","inputTokens":40}]},
		"metrics":{"latencyMs":1500}}`)
	in, outTokens, ok := converse.TokenCounts([]byte(`{"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":300}}`))
	if !ok || in != 310 || outTokens != 20 {
		t.Fatalf("token counts %d %d %v", in, outTokens, ok)
	}
}

// messagesStream is a Messages stream: thinking, text, a tool call, then
// the stop with the usage.
var messagesStream = [][2]string{
	{"message_start", `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":12,"cache_read_input_tokens":100,"output_tokens":1}}}`},
	{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":0}`},
	{"ping", `{"type":"ping"}`},
	{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":1}`},
	{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t1","name":"lookup","input":{}}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"hs\":"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":2}`},
	{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`},
	{"message_stop", `{"type":"message_stop"}`},
}

type frame struct {
	kind, name string // :message-type, and :event-type or :exception-type
	payload    string
}

// decode reads AWS's event stream back, checking its framing.
func decode(t *testing.T, b []byte) []frame {
	t.Helper()
	dec := eventstream.NewDecoder()
	r := bytes.NewReader(b)
	var out []frame
	for r.Len() > 0 {
		m, err := dec.Decode(r, nil)
		if err != nil {
			t.Fatal(err)
		}
		h := func(n string) string {
			if v := m.Headers.Get(n); v != nil {
				return v.String()
			}
			return ""
		}
		name := h(":event-type")
		if name == "" {
			name = h(":exception-type")
		}
		out = append(out, frame{h(":message-type"), name, string(m.Payload)})
	}
	return out
}

// A Messages stream becomes ConverseStream's events: tool use opens with
// contentBlockStart, text and reasoning with their first delta, as
// Bedrock does; the stop and the usage come last.
func TestConverseStream(t *testing.T) {
	s := converse.NewEventStream(false, time.Now())
	var out []byte
	for _, e := range messagesStream {
		out = s.Event(out, e[0], []byte(e[1]))
	}
	out = s.End(out)
	got := decode(t, out)
	want := []frame{
		{"event", "messageStart", `{"role":"assistant"}`},
		{"event", "contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"hmm"}}}`},
		{"event", "contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"signature":"s"}}}`},
		{"event", "contentBlockStop", `{"contentBlockIndex":0}`},
		{"event", "contentBlockDelta", `{"contentBlockIndex":1,"delta":{"text":"Hi"}}`},
		{"event", "contentBlockStop", `{"contentBlockIndex":1}`},
		{"event", "contentBlockStart", `{"contentBlockIndex":2,"start":{"toolUse":{"name":"lookup","toolUseId":"t1"}}}`},
		{"event", "contentBlockDelta", `{"contentBlockIndex":2,"delta":{"toolUse":{"input":"{\"hs\":"}}}`},
		{"event", "contentBlockStop", `{"contentBlockIndex":2}`},
		{"event", "messageStop", `{"stopReason":"tool_use"}`},
	}
	if len(got) != len(want)+1 {
		t.Fatalf("%d frames: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("frame %d: %+v, want %+v", i, got[i], w)
		}
	}
	last := got[len(want)]
	var meta struct {
		Usage   map[string]int64 `json:"usage"`
		Metrics struct {
			LatencyMs *int64 `json:"latencyMs"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(last.payload), &meta); err != nil || last.name != "metadata" || meta.Metrics.LatencyMs == nil {
		t.Fatalf("metadata %+v", last)
	}
	if u := meta.Usage; u["inputTokens"] != 12 || u["outputTokens"] != 9 || u["cacheReadInputTokens"] != 100 || u["totalTokens"] != 121 {
		t.Fatalf("usage %v", u)
	}

	// An error event ends the stream with an exception the SDKs know.
	s = converse.NewEventStream(false, time.Now())
	out = s.Event(nil, messagesStream[0][0], []byte(messagesStream[0][1]))
	out = s.Event(out, "error", []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	out = s.Event(out, messagesStream[7][0], []byte(messagesStream[7][1]))
	got = decode(t, s.End(out))
	if len(got) != 2 || got[1] != (frame{"exception", "serviceUnavailableException", `{"message":"Overloaded"}`}) {
		t.Fatalf("exception: %+v", got)
	}
}

// InvokeModel's stream carries each Messages event in a chunk, base64,
// the last with Bedrock's invocation metrics.
func TestInvokeStream(t *testing.T) {
	s := converse.NewEventStream(true, time.Now())
	var out []byte
	for _, e := range messagesStream {
		out = s.Event(out, e[0], []byte(e[1]))
	}
	got := decode(t, s.End(out))
	if len(got) != len(messagesStream)-1 { // no ping
		t.Fatalf("%d chunks", len(got))
	}
	for i, f := range got {
		var chunk struct {
			Bytes string `json:"bytes"`
		}
		if f.name != "chunk" || json.Unmarshal([]byte(f.payload), &chunk) != nil {
			t.Fatalf("chunk %d: %+v", i, f)
		}
		data, err := base64.StdEncoding.DecodeString(chunk.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if i < len(got)-1 {
			src := messagesStream[i]
			if i >= 5 {
				src = messagesStream[i+1]
			}
			if string(data) != src[1] {
				t.Fatalf("chunk %d: %s, want %s", i, data, src[1])
			}
			continue
		}
		var stop struct {
			Type    string           `json:"type"`
			Metrics map[string]int64 `json:"amazon-bedrock-invocationMetrics"`
		}
		if err := json.Unmarshal(data, &stop); err != nil || stop.Type != "message_stop" || stop.Metrics["inputTokenCount"] != 12 ||
			stop.Metrics["outputTokenCount"] != 9 || stop.Metrics["cacheReadInputTokenCount"] != 100 {
			t.Fatalf("last chunk %s", data)
		}
	}
}

// Errors take AWS's shape: the exception named for the status, the
// message kept, Anthropic's 529 as 503.
func TestClientError(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   int
		kind   string
		msg    string
	}{
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, 400, "ValidationException", "bad"},
		{401, `{"type":"error","error":{"message":"invalid key"}}`, 401, "AccessDeniedException", "invalid key"},
		{429, `{"error":{"message":"slow down"}}`, 429, "ThrottlingException", "slow down"},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, 503, "ServiceUnavailableException", "Overloaded"},
		{502, `not json`, 502, "ServiceUnavailableException", "Bad Gateway"},
		{404, `{"message":"no model"}`, 404, "ResourceNotFoundException", "no model"},
	} {
		status, kind, body := converse.ClientError(c.status, []byte(c.body))
		if status != c.want || kind != c.kind || string(body) != `{"message":"`+c.msg+`"}` {
			t.Errorf("%d: %d %s %s", c.status, status, kind, body)
		}
	}
}
