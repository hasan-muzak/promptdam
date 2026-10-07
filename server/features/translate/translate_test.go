package translate

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// sameJSON fails unless got and want are the same JSON value.
func sameJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestChatToMessages(t *testing.T) {
	in := `{
		"model": "gpt-5", "max_completion_tokens": 500, "temperature": 1.5, "top_p": 0.9, "stop": "END", "seed": 7,
		"user": "u-42", "parallel_tool_calls": false, "tool_choice": "required", "stream": true,
		"stream_options": {"include_usage": true}, "logit_bias": null,
		"response_format": {"type": "json_schema", "json_schema": {"name": "x", "schema": {"type": "object"}}},
		"messages": [
			{"role": "system", "content": "Be brief."},
			{"role": "developer", "content": [{"type": "text", "text": "Use metric units."}]},
			{"role": "user", "content": [
				{"type": "text", "text": "What is in these?"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBOR"}},
				{"type": "image_url", "image_url": {"url": "https://example.com/a.jpg", "detail": "high"}},
				{"type": "file", "file": {"file_data": "data:application/pdf;base64,JVBER", "filename": "a.pdf"}}
			]},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "weather", "arguments": "{\"city\":\"Oslo\"}"}},
				{"id": "call.2", "type": "function", "function": {"name": "time", "arguments": "not json"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "12C"},
			{"role": "tool", "tool_call_id": "call.2", "content": [{"type": "text", "text": "noon"}]},
			{"role": "user", "content": "Thanks"}
		],
		"tools": [{"type": "function", "function": {"name": "weather", "description": "Weather now",
			"parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}},
			{"type": "function", "function": {"name": "time"}}]
	}`
	out, dropped, err := Request(wire.OpenAIChat, wire.AnthropicMessages, []byte(in), Options{Model: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{
		"model": "claude-sonnet-5-5", "max_tokens": 500,
		"system": [{"type": "text", "text": "Be brief."}, {"type": "text", "text": "Use metric units."}],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "What is in these?"},
				{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBOR"}},
				{"type": "image", "source": {"type": "url", "url": "https://example.com/a.jpg"}},
				{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "JVBER"}, "title": "a.pdf"}
			]},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "call_1", "name": "weather", "input": {"city": "Oslo"}},
				{"type": "tool_use", "id": "call_2", "name": "time", "input": {"arguments": "not json"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_1", "content": "12C"},
				{"type": "tool_result", "tool_use_id": "call_2", "content": "noon"},
				{"type": "text", "text": "Thanks"}
			]}
		],
		"tools": [
			{"name": "weather", "description": "Weather now", "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}},
			{"name": "time", "input_schema": {"type": "object", "properties": {}}}
		],
		"tool_choice": {"type": "any", "disable_parallel_tool_use": true},
		"temperature": 1, "stop_sequences": ["END"], "stream": true, "metadata": {"user_id": "u-42"},
		"output_config": {"format": {"type": "json_schema", "schema": {"type": "object"}}}
	}`)
	if want := []string{"seed", "top_p"}; !reflect.DeepEqual(dropped, want) {
		t.Fatalf("dropped %v, want %v", dropped, want)
	}
}

func TestChatToMessagesDefaults(t *testing.T) {
	out, _, err := Request(wire.OpenAIChat, wire.AnthropicMessages, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],
		"response_format":{"type":"json_object"},"reasoning_effort":"minimal"}`), Options{Model: "claude-haiku-4-5", Reasoning: true})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"model":"claude-haiku-4-5","max_tokens":8192,
		"system":[{"type":"text","text":"`+jsonInstruction+`"}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"output_config":{"effort":"low"}}`)

	_, dropped, err := Request(wire.OpenAIChat, wire.AnthropicMessages, []byte(`{"model":"m","messages":[],"reasoning_effort":"high"}`),
		Options{Model: "x"})
	if err != nil || !reflect.DeepEqual(dropped, []string{"reasoning_effort"}) {
		t.Fatalf("a model without reasoning drops the effort: %v %v", dropped, err)
	}
}

func TestChatToMessagesRefuses(t *testing.T) {
	for name, body := range map[string]string{
		"several answers": `{"model":"m","n":2,"messages":[]}`,
		"audio":           `{"model":"m","modalities":["text","audio"],"messages":[]}`,
		"audio part":      `{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{}}]}]}`,
		"file by id":      `{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"f"}}]}]}`,
		"not a pdf":       `{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:text/csv;base64,YQ=="}}]}]}`,
		"custom tool":     `{"model":"m","messages":[],"tools":[{"type":"custom","custom":{"name":"x"}}]}`,
		"function role":   `{"model":"m","messages":[{"role":"function","name":"f","content":"x"}]}`,
		"allowed tools":   `{"model":"m","messages":[],"tool_choice":{"type":"allowed_tools"}}`,
		"not an object":   `[1]`,
	} {
		_, _, err := Request(wire.OpenAIChat, wire.AnthropicMessages, []byte(body), Options{Model: "x"})
		var te *Error
		if !errors.As(err, &te) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
}

func TestMessagesToChat(t *testing.T) {
	in := `{
		"model": "claude-opus-5-5", "max_tokens": 1024, "top_k": 5, "temperature": 0.2,
		"system": [{"type": "text", "text": "You are terse.", "cache_control": {"type": "ephemeral"}}],
		"stop_sequences": ["###"], "stream": true, "metadata": {"user_id": "dev-1"},
		"thinking": {"type": "enabled", "budget_tokens": 2000},
		"tool_choice": {"type": "any", "disable_parallel_tool_use": true},
		"output_config": {"format": {"type": "json_schema", "schema": {"type": "object"}}},
		"tools": [{"name": "read", "description": "Read a file", "input_schema": {"type": "object"}}],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Read it"},
				{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "/9j/"}},
				{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "JVBER"}}
			]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "hmm", "signature": "sig"},
				{"type": "text", "text": "Reading."},
				{"type": "tool_use", "id": "toolu_1", "name": "read", "input": {"path": "a.go"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "is_error": true, "content": [
					{"type": "text", "text": "denied"}, {"type": "image", "source": {"type": "url", "url": "https://x/y.png"}}]},
				{"type": "text", "text": "Try again"}
			]}
		]
	}`
	out, dropped, err := Request(wire.AnthropicMessages, wire.OpenAIChat, []byte(in), Options{Model: "gpt-5", Kind: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{
		"model": "gpt-5", "max_completion_tokens": 1024, "temperature": 0.2, "stop": ["###"], "stream": true,
		"stream_options": {"include_usage": true}, "user": "dev-1",
		"tools": [{"type": "function", "function": {"name": "read", "description": "Read a file", "parameters": {"type": "object"}}}],
		"tool_choice": "required", "parallel_tool_calls": false,
		"response_format": {"type": "json_schema", "json_schema": {"name": "output", "schema": {"type": "object"}, "strict": true}},
		"messages": [
			{"role": "system", "content": "You are terse."},
			{"role": "user", "content": [
				{"type": "text", "text": "Read it"},
				{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,/9j/"}},
				{"type": "file", "file": {"filename": "document.pdf", "file_data": "data:application/pdf;base64,JVBER"}}
			]},
			{"role": "assistant", "content": "Reading.", "tool_calls": [
				{"id": "toolu_1", "type": "function", "function": {"name": "read", "arguments": "{\"path\": \"a.go\"}"}}]},
			{"role": "tool", "tool_call_id": "toolu_1", "content": "Error: denied"},
			{"role": "user", "content": [
				{"type": "text", "text": "The result of tool call toolu_1 included these images:"},
				{"type": "image_url", "image_url": {"url": "https://x/y.png"}},
				{"type": "text", "text": "Try again"}
			]}
		]
	}`)
	if want := []string{"thinking", "top_k"}; !reflect.DeepEqual(dropped, want) {
		t.Fatalf("dropped %v, want %v", dropped, want)
	}

	out, _, err = Request(wire.AnthropicMessages, wire.OpenAIChat, []byte(`{"model":"c","max_tokens":10,"system":"S",
		"thinking":{"type":"enabled","budget_tokens":20000},"messages":[{"role":"user","content":"hi"}]}`),
		Options{Model: "deepseek-reasoner", Kind: "deepseek", Reasoning: true})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"model":"deepseek-reasoner","max_tokens":10,"reasoning_effort":"high",
		"messages":[{"role":"system","content":"S"},{"role":"user","content":"hi"}]}`)
}

func TestMessagesToChatRefuses(t *testing.T) {
	for name, body := range map[string]string{
		"prefill":     `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"{"}]}`,
		"server tool": `{"model":"m","max_tokens":1,"messages":[],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
		"mcp":         `{"model":"m","max_tokens":1,"messages":[],"mcp_servers":[{"url":"https://x"}]}`,
		"file image":  `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"f"}}]}]}`,
		"search":      `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"search_result"}]}]}`,
	} {
		_, _, err := Request(wire.AnthropicMessages, wire.OpenAIChat, []byte(body), Options{Model: "x"})
		var te *Error
		if !errors.As(err, &te) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
}

func TestResponses(t *testing.T) {
	out, err := Response(wire.AnthropicMessages, wire.OpenAIChat, []byte(`{"id":"msg_1","type":"message","role":"assistant",
		"model":"claude-sonnet-5-5","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"Hi"},
		{"type":"tool_use","id":"toolu_1","name":"f","input":{"a":1}}],"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":20,"cache_creation_input_tokens":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	var cc chatCompletion
	_ = json.Unmarshal(out, &cc)
	cc.Created = 0
	got, _ := json.Marshal(cc)
	sameJSON(t, got, `{"id":"msg_1","object":"chat.completion","created":0,"model":"claude-sonnet-5-5","choices":[{"index":0,
		"message":{"role":"assistant","content":"Hi","refusal":null,"tool_calls":[{"id":"toolu_1","type":"function",
		"function":{"name":"f","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls","logprobs":null}],
		"usage":{"prompt_tokens":33,"completion_tokens":5,"total_tokens":38,"prompt_tokens_details":{"cached_tokens":20}}}`)

	out, err = Response(wire.OpenAIChat, wire.AnthropicMessages, []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-5",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Done","tool_calls":[{"id":"call_1","type":"function",
		"function":{"name":"f","arguments":"{\"b\":2}"}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":30,"completion_tokens":7,"total_tokens":37,"prompt_tokens_details":{"cached_tokens":10}}}`))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"id":"chatcmpl-1","type":"message","role":"assistant","model":"gpt-5","content":[{"type":"text","text":"Done"},
		{"type":"tool_use","id":"call_1","name":"f","input":{"b":2}}],"stop_reason":"tool_use","stop_sequence":null,
		"usage":{"input_tokens":20,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":10}}`)

	out, _ = Response(wire.OpenAIChat, wire.AnthropicMessages, []byte(`{"id":"c","model":"m","choices":[{"message":{"content":null,
		"refusal":"I can't help with that."},"finish_reason":"stop"}]}`))
	if !strings.Contains(string(out), `"stop_reason":"refusal"`) || !strings.Contains(string(out), "I can't help") {
		t.Fatalf("a refusal stays one: %s", out)
	}
	out, _ = Response(wire.AnthropicMessages, wire.OpenAIChat, []byte(`{"id":"m","content":[{"type":"text","text":"No."}],
		"stop_reason":"refusal","usage":{}}`))
	if !strings.Contains(string(out), `"refusal":"No."`) || !strings.Contains(string(out), `"content":null`) ||
		!strings.Contains(string(out), `"finish_reason":"content_filter"`) {
		t.Fatalf("a refusal stays one: %s", out)
	}
}

func TestErrorBody(t *testing.T) {
	sameJSON(t, ErrorBody(wire.AnthropicMessages, 429, []byte(`{"error":{"message":"slow down","type":"requests","code":"rate_limit_exceeded"}}`)),
		`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	sameJSON(t, ErrorBody(wire.OpenAIChat, 400, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)),
		`{"error":{"message":"bad","type":"invalid_request_error","code":null,"param":null}}`)
	sameJSON(t, ErrorBody(wire.OpenAIChat, 502, []byte(`<html>`)),
		`{"error":{"message":"the provider answered 502 Bad Gateway","type":"api_error","code":null,"param":null}}`)
}

// events splits an SSE stream into its events.
func events(t *testing.T, stream string) []wire.Event {
	t.Helper()
	s := wire.NewScanner(strings.NewReader(stream), 0)
	var out []wire.Event
	for s.Next() {
		ev := s.Event()
		ev.Data = append([]byte(nil), ev.Data...)
		ev.Raw = append([]byte(nil), ev.Raw...)
		out = append(out, ev)
	}
	return out
}

func TestStreamMessagesToChat(t *testing.T) {
	in := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-haiku-4-5\",\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":4,\"output_tokens\":1}}}\n\n" +
		"event: ping\ndata: {\"type\":\"ping\"}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_9\",\"name\":\"f\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"a\\\":\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"1}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":9}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	s, err := NewStream(wire.AnthropicMessages, wire.OpenAIChat, true)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, ev := range events(t, in) {
		out = s.Event(out, ev)
	}
	out = s.End(out)

	// Read it back the way the gateway's meter and an OpenAI SDK would.
	meter := wire.NewMeter(wire.OpenAIChat)
	var content, args, finish, id string
	var done bool
	for _, ev := range events(t, string(out)) {
		meter.Event(ev)
		if string(ev.Data) == "[DONE]" {
			done = true
			continue
		}
		if len(ev.Data) == 0 {
			continue // the ping, as a comment
		}
		var c chunk
		if err := json.Unmarshal(ev.Data, &c); err != nil {
			t.Fatalf("chunk %s: %v", ev.Data, err)
		}
		for _, ch := range c.Choices {
			if ch.Delta.Content != nil {
				content += *ch.Delta.Content
			}
			for _, tc := range ch.Delta.ToolCalls {
				if tc.ID != "" {
					id = tc.ID
				}
				args += tc.Function.Arguments
			}
			if ch.FinishReason != nil {
				finish = *ch.FinishReason
			}
		}
	}
	u, ok := meter.Usage()
	if content != "Hello" || args != `{"a":1}` || id != "toolu_9" || finish != "tool_calls" || !done {
		t.Fatalf("content %q args %q id %q finish %q done %v\n%s", content, args, id, finish, done, out)
	}
	if !ok || u.Input != 16 || u.CacheRead != 4 || u.Output != 9 { // the meter counts cache reads as input too
		t.Fatalf("usage %+v (%v)", u, ok)
	}
}

func TestStreamChatToMessages(t *testing.T) {
	in := "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Let me\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" check.\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_b\",\"type\":\"function\",\"function\":{\"name\":\"g\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"x\\\":\"}}]}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"2}\"}}]}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5\",\"choices\":[],\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":8,\"total_tokens\":58,\"prompt_tokens_details\":{\"cached_tokens\":30}}}\n\n" +
		"data: [DONE]\n\n"
	s, err := NewStream(wire.OpenAIChat, wire.AnthropicMessages, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, ev := range events(t, in) {
		out = s.Event(out, ev)
	}
	out = s.End(out)

	meter := wire.NewMeter(wire.AnthropicMessages)
	var names []string
	var textOut string
	inputs := map[int]string{}
	tools := map[int]string{}
	stop := ""
	for _, ev := range events(t, string(out)) {
		meter.Event(ev)
		names = append(names, ev.Name)
		var e struct {
			Index        int `json:"index"`
			ContentBlock *struct {
				Type, ID, Name string
			} `json:"content_block"`
			Delta *struct {
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(ev.Data, &e); err != nil {
			t.Fatalf("event %s: %v", ev.Data, err)
		}
		if e.ContentBlock != nil && e.ContentBlock.Type == "tool_use" {
			tools[e.Index] = e.ContentBlock.ID + ":" + e.ContentBlock.Name
		}
		if e.Delta != nil {
			textOut += e.Delta.Text
			inputs[e.Index] += e.Delta.PartialJSON
			if e.Delta.StopReason != "" {
				stop = e.Delta.StopReason
			}
		}
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop", "content_block_start", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("events %v\nwant %v\n%s", names, want, out)
	}
	if textOut != "Let me check." || tools[1] != "call_a:f" || tools[2] != "call_b:g" || inputs[1] != `{"x":2}` || inputs[2] != `{}` ||
		stop != "tool_use" {
		t.Fatalf("text %q tools %v inputs %v stop %q", textOut, tools, inputs, stop)
	}
	u, ok := meter.Usage()
	if !ok || u.Input != 50 || u.CacheRead != 30 || u.Output != 8 {
		t.Fatalf("usage %+v (%v)", u, ok)
	}
}

func TestStreamCutShort(t *testing.T) {
	s, _ := NewStream(wire.AnthropicMessages, wire.OpenAIChat, false)
	out := s.Event(nil, wire.Event{Data: []byte(`{"type":"message_start","message":{"id":"m","model":"x"}}`)})
	out = s.End(out)
	if strings.Contains(string(out), "[DONE]") || strings.Contains(string(out), "finish_reason\":\"") {
		t.Fatalf("a cut stream must stay cut: %s", out)
	}

	s, _ = NewStream(wire.OpenAIChat, wire.AnthropicMessages, false)
	out = s.Event(nil, wire.Event{Data: []byte(`{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"a"}}]}`)})
	out = s.End(out)
	if strings.Contains(string(out), "message_stop") {
		t.Fatalf("a cut stream must stay cut: %s", out)
	}
	// A provider that sends its finish reason but no [DONE] still ends.
	out = s.Event(nil, wire.Event{Data: []byte(`{"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)})
	out = s.End(out)
	if !strings.Contains(string(out), "message_stop") {
		t.Fatalf("a finished stream ends: %s", out)
	}
}

// A request survives the trip to the other format and back.
func TestRoundTrip(t *testing.T) {
	in := `{"model":"gpt-5","max_tokens":300,"messages":[{"role":"system","content":"S"},{"role":"user","content":"Q"},
		{"role":"assistant","content":"A","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"k\":\"v\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"R"},{"role":"user","content":"Q2"}],
		"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"tool_choice":"auto"}`
	mid, _, err := Request(wire.OpenAIChat, wire.AnthropicMessages, []byte(in), Options{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := Request(wire.AnthropicMessages, wire.OpenAIChat, mid, Options{Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, back, `{"model":"gpt-5","max_tokens":300,"messages":[{"role":"system","content":"S"},{"role":"user","content":"Q"},
		{"role":"assistant","content":"A","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"k\":\"v\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"R"},{"role":"user","content":"Q2"}],
		"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"tool_choice":"auto"}`)
}
