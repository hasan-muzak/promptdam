package converse_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/converse"
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

// A Messages request becomes Converse's: the system prompt and messages as
// blocks (one role's consecutive messages joined), the sampling in
// inferenceConfig, tools and their uses and results, and what the model
// can't take listed as dropped.
func TestRequest(t *testing.T) {
	in := `{"model":"x","max_tokens":300,"stream":true,"system":"Be brief.","temperature":0.2,"top_p":0.9,
		"stop_sequences":["###"],"service_tier":"auto","metadata":{"user_id":"dev-1"},
		"tools":[{"name":"ls","description":"List files","input_schema":{"type":"object","properties":{"dir":{"type":"string"}}}}],
		"tool_choice":{"type":"any"},
		"messages":[
			{"role":"user","content":"list them"},
			{"role":"user","content":[{"type":"text","text":"in src"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]},
			{"role":"assistant","content":[{"type":"text","text":"Listing."},{"type":"tool_use","id":"t1","name":"ls","input":{"dir":"src"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.go"},
				{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":[]},
				{"type":"document","title":"Spec: v2!","source":{"type":"text","media_type":"text/plain","data":"hi"}}]}
		]}`
	out, dropped, err := converse.Request([]byte(in), "us.meta.llama3-3-70b-instruct-v1:0")
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{
		"system":[{"text":"Be brief."}],
		"inferenceConfig":{"maxTokens":300,"temperature":0.2,"topP":0.9,"stopSequences":["###"]},
		"requestMetadata":{"user_id":"dev-1"},
		"toolConfig":{"tools":[{"toolSpec":{"name":"ls","description":"List files",
			"inputSchema":{"json":{"type":"object","properties":{"dir":{"type":"string"}}}}}}],"toolChoice":{"any":{}}},
		"messages":[
			{"role":"user","content":[{"text":"list them"},{"text":"in src"},{"image":{"format":"png","source":{"bytes":"iVBOR"}}}]},
			{"role":"assistant","content":[{"text":"Listing."},{"toolUse":{"toolUseId":"t1","name":"ls","input":{"dir":"src"}}}]},
			{"role":"user","content":[{"toolResult":{"toolUseId":"t1","content":[{"text":"a.go"}]}},
				{"toolResult":{"toolUseId":"t2","content":[{"json":{}}],"status":"error"}},
				{"document":{"format":"txt","name":"Spec v2","source":{"bytes":"aGk="}}}]}
		]}`)
	if strings.Join(dropped, ",") != "service_tier" {
		t.Fatalf("dropped %v", dropped)
	}
}

// What each model family takes: Claude its thinking (signatures kept),
// top_k, effort and checkpoints with their lifetimes, anywhere; Nova
// checkpoints of five minutes, not on tools; Llama none of these.
func TestRequestByFamily(t *testing.T) {
	in := `{"max_tokens":9,"top_k":40,"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},
		"system":[{"type":"text","text":"Long rules.","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"tools":[{"name":"ls","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":"a"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"sig"},{"type":"text","text":"b"}]},
			{"role":"user","content":[{"type":"text","text":"c","cache_control":{"type":"ephemeral"}}]}]}`
	claude, dropped, err := converse.Request([]byte(in), "anthropic.claude-sonnet-4-5-20250929-v1:0")
	if err != nil || len(dropped) != 0 {
		t.Fatalf("%v %v", err, dropped)
	}
	sameJSON(t, claude, `{"inferenceConfig":{"maxTokens":9},
		"system":[{"text":"Long rules."},{"cachePoint":{"type":"default","ttl":"1h"}}],
		"toolConfig":{"tools":[{"toolSpec":{"name":"ls","inputSchema":{"json":{"type":"object"}}}},{"cachePoint":{"type":"default"}}]},
		"additionalModelRequestFields":{"top_k":40,"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}},
		"messages":[{"role":"user","content":[{"text":"a"}]},
			{"role":"assistant","content":[{"reasoningContent":{"reasoningText":{"text":"hmm","signature":"sig"}}},{"text":"b"}]},
			{"role":"user","content":[{"text":"c"},{"cachePoint":{"type":"default"}}]}]}`)

	nova, dropped, err := converse.Request([]byte(in), "us.amazon.nova-lite-v1:0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dropped, ",") != "cache_control,output_config.effort,thinking,thinking blocks,top_k" {
		t.Fatalf("nova dropped %v", dropped)
	}
	sameJSON(t, nova, `{"inferenceConfig":{"maxTokens":9},
		"system":[{"text":"Long rules."},{"cachePoint":{"type":"default"}}],
		"toolConfig":{"tools":[{"toolSpec":{"name":"ls","inputSchema":{"json":{"type":"object"}}}}]},
		"messages":[{"role":"user","content":[{"text":"a"}]},{"role":"assistant","content":[{"text":"b"}]},
			{"role":"user","content":[{"text":"c"},{"cachePoint":{"type":"default"}}]}]}`)

	llama, _, err := converse.Request([]byte(in), "meta.llama3-3-70b-instruct-v1:0")
	if err != nil || bytes.Contains(llama, []byte("cachePoint")) || bytes.Contains(llama, []byte("reasoning")) {
		t.Fatalf("llama %s %v", llama, err)
	}

	// Four checkpoints at most, Converse's limit.
	many := `{"max_tokens":9,"messages":[` + strings.Repeat(`{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":"y"},`, 5) +
		`{"role":"user","content":"z"}]}`
	out, dropped, _ := converse.Request([]byte(many), "anthropic.claude-haiku-4-5")
	if n := bytes.Count(out, []byte("cachePoint")); n != 4 || strings.Join(dropped, ",") != "cache_control" {
		t.Fatalf("%d checkpoints, dropped %v", n, dropped)
	}
}

// What Converse can't express is refused, so the gateway tries another
// target; a conversation that used tools names them even when the request
// offers none.
func TestRequestRefusesAndFills(t *testing.T) {
	for name, in := range map[string]string{
		"an image by URL":             `{"max_tokens":9,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://x/y.png"}}]}]}`,
		"a server tool":               `{"max_tokens":9,"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"a"}]}`,
		"a tool choice without tools": `{"max_tokens":9,"tool_choice":{"type":"tool","name":"ls"},"messages":[{"role":"user","content":"a"}]}`,
	} {
		var te *translate.Error
		if _, _, err := converse.Request([]byte(in), "amazon.nova-lite-v1:0"); !errors.As(err, &te) {
			t.Errorf("%s: %v", name, err)
		}
	}
	used := `{"max_tokens":9,"messages":[{"role":"user","content":"a"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ls","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`
	out, _, err := converse.Request([]byte(used), "amazon.nova-lite-v1:0")
	if err != nil || !bytes.Contains(out, []byte(`"toolConfig":{"tools":[{"toolSpec":{"name":"ls","inputSchema":{"json":{"type":"object"}}}}]}`)) {
		t.Fatalf("%s %v", out, err)
	}
}

// A Converse answer reads as a Messages answer: its blocks, stop reason
// and usage (inputTokens is Anthropic's input_tokens: neither excludes
// what the cache served).
func TestResponse(t *testing.T) {
	body := `{"output":{"message":{"role":"assistant","content":[
		{"reasoningContent":{"reasoningText":{"text":"think","signature":"s1"}}},
		{"text":"Listing."},
		{"toolUse":{"toolUseId":"t1","name":"ls","input":{"dir":"src"}}}]}},
		"stopReason":"tool_use","metrics":{"latencyMs":120},
		"usage":{"inputTokens":20,"outputTokens":7,"totalTokens":1527,"cacheReadInputTokens":1000,"cacheWriteInputTokens":500,
			"cacheDetails":[{"inputTokens":500,"ttl":"1h"}]}}`
	h := http.Header{"X-Amzn-Requestid": {"ab-12"}}
	out, err := converse.Response([]byte(body), "amazon.nova-lite-v1:0", converse.MessageID(h))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"id":"msg_bdrk_ab12","type":"message","role":"assistant","model":"amazon.nova-lite-v1:0",
		"content":[{"type":"thinking","thinking":"think","signature":"s1"},{"type":"text","text":"Listing."},
			{"type":"tool_use","id":"t1","name":"ls","input":{"dir":"src"}}],
		"stop_reason":"tool_use","stop_sequence":null,
		"usage":{"input_tokens":20,"output_tokens":7,"cache_read_input_tokens":1000,"cache_creation_input_tokens":500,
			"cache_creation":{"ephemeral_1h_input_tokens":500,"ephemeral_5m_input_tokens":0}}}`)
	m := wire.NewMeter(wire.AnthropicMessages)
	m.Body(out)
	if u, ok := m.Usage(); !ok || u.Input != 1520 || u.CacheRead != 1000 || u.CacheWrite != 500 || u.Output != 7 {
		t.Fatalf("metered %+v", u)
	}
}

// AWS's errors read as Anthropic's, named by AWS's type; a model's own
// error is the provider's failure (a 502, so a fallback takes the call).
func TestError(t *testing.T) {
	status, body := converse.Error(400, http.Header{"X-Amzn-Errortype": {"ValidationException:http://internal.amazon.com/coral/com.amazon.bedrock/"}},
		[]byte(`{"message":"Input is too long for requested model."}`))
	sameJSON(t, body, `{"type":"error","error":{"type":"invalid_request_error","message":"ValidationException: Input is too long for requested model."}}`)
	if status != 400 {
		t.Fatal(status)
	}
	status, body = converse.Error(424, http.Header{}, []byte(`{"Message":"The model failed","__type":"com.amazon#ModelErrorException"}`))
	if status != 502 || !bytes.Contains(body, []byte(`"ModelErrorException: The model failed"`)) {
		t.Fatalf("%d %s", status, body)
	}
	if _, body = converse.Error(429, http.Header{"X-Amzn-Errortype": {"ThrottlingException"}}, nil); !bytes.Contains(body, []byte("rate_limit_error")) {
		t.Fatalf("%s", body)
	}
}

// frames encodes Converse stream events as AWS does.
func frames(t *testing.T, events ...[2]string) io.ReadCloser {
	t.Helper()
	var b bytes.Buffer
	enc := eventstream.NewEncoder()
	for _, e := range events {
		var h eventstream.Headers
		if kind, ok := strings.CutPrefix(e[0], "!"); ok {
			h.Set(":message-type", eventstream.StringValue("exception"))
			h.Set(":exception-type", eventstream.StringValue(kind))
		} else {
			h.Set(":message-type", eventstream.StringValue("event"))
			h.Set(":event-type", eventstream.StringValue(e[0]))
		}
		h.Set(":content-type", eventstream.StringValue("application/json"))
		if err := enc.Encode(&b, eventstream.Message{Headers: h, Payload: []byte(e[1])}); err != nil {
			t.Fatal(err)
		}
	}
	return io.NopCloser(&b)
}

// events reads Anthropic's events back, by name, and meters them.
func events(t *testing.T, r io.Reader) ([]string, []map[string]any, *wire.Meter) {
	t.Helper()
	var names []string
	var data []map[string]any
	m := wire.NewMeter(wire.AnthropicMessages)
	s := wire.NewScanner(r, 0)
	for s.Next() {
		ev := s.Event()
		m.Event(ev)
		var d map[string]any
		if err := json.Unmarshal(ev.Data, &d); err != nil || d["type"] != ev.Name {
			t.Fatalf("event %s: %s", ev.Name, ev.Data)
		}
		names = append(names, ev.Name)
		data = append(data, d)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return names, data, m
}

// A Converse stream reads as Anthropic's: blocks opened on their first
// delta, a tool's input streamed as JSON pieces, thinking with its
// signature, the stop held until the usage that follows it.
func TestStream(t *testing.T) {
	body := frames(t,
		[2]string{"messageStart", `{"role":"assistant","p":"abcdefgh"}`},
		[2]string{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"Let me "}}}`},
		[2]string{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"signature":"sig"}}}`},
		[2]string{"contentBlockStop", `{"contentBlockIndex":0}`},
		[2]string{"contentBlockDelta", `{"contentBlockIndex":1,"delta":{"text":"Listing"}}`},
		[2]string{"contentBlockDelta", `{"contentBlockIndex":1,"delta":{"text":" now."}}`},
		[2]string{"contentBlockStop", `{"contentBlockIndex":1}`},
		[2]string{"contentBlockStart", `{"contentBlockIndex":2,"start":{"toolUse":{"toolUseId":"t1","name":"ls"}}}`},
		[2]string{"contentBlockDelta", `{"contentBlockIndex":2,"delta":{"toolUse":{"input":"{\"dir\":"}}}`},
		[2]string{"contentBlockDelta", `{"contentBlockIndex":2,"delta":{"toolUse":{"input":"\"src\"}"}}}`},
		[2]string{"contentBlockStop", `{"contentBlockIndex":2}`},
		[2]string{"messageStop", `{"stopReason":"tool_use"}`},
		[2]string{"metadata", `{"usage":{"inputTokens":12,"outputTokens":30,"totalTokens":542,"cacheReadInputTokens":500},"metrics":{"latencyMs":800}}`},
	)
	r := converse.Stream(body, "anthropic.claude-haiku-4-5", "msg_1")
	names, data, m := events(t, r)
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v", names)
	}
	if cb := data[1]["content_block"].(map[string]any); cb["type"] != "thinking" {
		t.Fatalf("thinking %v", data[1])
	}
	if d := data[3]["delta"].(map[string]any); d["type"] != "signature_delta" || d["signature"] != "sig" {
		t.Fatalf("signature %v", data[3])
	}
	if cb := data[9]["content_block"].(map[string]any); cb["type"] != "tool_use" || cb["id"] != "t1" || cb["name"] != "ls" {
		t.Fatalf("tool %v", data[9])
	}
	if d := data[13]["delta"].(map[string]any); d["stop_reason"] != "tool_use" {
		t.Fatalf("stop %v", data[13])
	}
	if u, ok := m.Usage(); !ok || u.Input != 512 || u.CacheRead != 500 || u.Output != 30 || m.Model() != "anthropic.claude-haiku-4-5" {
		t.Fatalf("metered %+v %q", u, m.Model())
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

// A stream that fails says so in an error event: an AWS exception mid-way
// (throttling is Anthropic's overloaded_error), or an end before its stop.
func TestStreamFailures(t *testing.T) {
	throttled := frames(t,
		[2]string{"messageStart", `{"role":"assistant"}`},
		[2]string{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Hi"}}`},
		[2]string{"!throttlingException", `{"message":"Too many requests, please wait before trying again."}`},
	)
	names, data, m := events(t, converse.Stream(throttled, "amazon.nova-lite-v1:0", "msg_1"))
	last := data[len(data)-1]["error"].(map[string]any)
	if names[len(names)-1] != "error" || last["type"] != "overloaded_error" || m.StreamError() != "overloaded_error" {
		t.Fatalf("throttled %v %v", names, last)
	}

	cut := frames(t,
		[2]string{"messageStart", `{"role":"assistant"}`},
		[2]string{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Hi"}}`},
	)
	names, _, _ = events(t, converse.Stream(cut, "amazon.nova-lite-v1:0", "msg_1"))
	if names[len(names)-1] != "error" {
		t.Fatalf("cut short %v", names)
	}

	// Stopped, its usage never sent: the message still ends.
	unmetered := frames(t,
		[2]string{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Hi"}}`},
		[2]string{"messageStop", `{"stopReason":"end_turn"}`},
	)
	names, _, _ = events(t, converse.Stream(unmetered, "amazon.nova-lite-v1:0", "msg_1"))
	if strings.Join(names, ",") != "message_start,content_block_start,content_block_delta,content_block_stop,message_delta,message_stop" {
		t.Fatalf("unmetered %v", names)
	}
}
