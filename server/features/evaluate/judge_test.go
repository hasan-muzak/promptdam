package evaluate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

func TestCheck(t *testing.T) {
	text := func(s string) Answer { return Answer{Text: s} }
	tool := func(name, args string) Answer {
		return Answer{Tools: []ToolCall{{Name: name, Args: json.RawMessage(args)}}}
	}
	for _, tt := range []struct {
		name       string
		base, cand Answer
		json       bool
		verdict    string
		how        string
		decided    bool
	}{
		{"same words", text("The total is 42."), text("  The total   is 42.\n"), false, Tie, CheckSame, true},
		{"different words", text("The total is 42."), text("It's 42."), false, "", "", false},
		{"the candidate refuses", text("Here."), Answer{Text: "No.", Refused: true}, false, Loss, CheckRefusal, true},
		{"both refuse", Answer{Refused: true}, Answer{Refused: true}, false, Tie, CheckSame, true},
		{"nothing", text("Here."), text(" "), false, Loss, CheckEmpty, true},
		{"JSON asked, prose given", text(`{"total": 42}`), text("The total is 42"), true, Loss, CheckJSON, true},
		{"fenced JSON is JSON", text(`{"total": 42}`), text("```json\n{\"total\": 41}\n```"), true, "", "", false},
		{"the same call", tool("weather", `{"city":"Oslo"}`), tool("weather", `{ "city" : "Oslo" }`), false, Tie, CheckSame, true},
		{"another tool", tool("weather", `{}`), tool("time", `{}`), false, Loss, CheckTools, true},
		{"no tool", tool("weather", `{}`), text("It's sunny."), false, Loss, CheckTools, true},
		{"other arguments", tool("weather", `{"city":"Oslo"}`), tool("weather", `{"city":"Bergen"}`), false, "", "", false},
		{"a tool the default didn't call", text("It's sunny."), tool("weather", `{}`), false, "", "", false},
	} {
		v, how, ok := check(tt.base, tt.cand, tt.json)
		if v != tt.verdict || how != tt.how || ok != tt.decided {
			t.Errorf("%s: %q %q %v", tt.name, v, how, ok)
		}
	}
}

func TestReadVerdict(t *testing.T) {
	for in, want := range map[string]string{
		`{"better": "a"}`: "a", ` {"better":"tie"} `: "tie", "```json\n{\"better\": \"B\"}\n```": "b",
		`I think {"better": "b"} fits.`: "b", `{"better": "c"}`: "", "A is better": "",
	} {
		if got := readVerdict(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestJudgePrompt(t *testing.T) {
	p := judgePrompt("Be brief.", strings.Repeat("é", maxRequest), Answer{Text: "A"}, Answer{Tools: []ToolCall{{Name: "f", Args: json.RawMessage(`{}`)}}})
	for _, want := range []string{"<instructions>\nBe brief.", "[…cut]", "<answer_a>\nA\n</answer_a>", "[calls tool f with {}]"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	if !strings.Contains(p, "<last_message>") || strings.Contains(p, "�") {
		t.Fatal("cut inside a character")
	}
}

func TestAnswerOf(t *testing.T) {
	for _, tt := range []struct {
		name   string
		f      wire.Format
		stream bool
		body   string
		want   Answer
	}{
		{"chat", wire.OpenAIChat, false, `{"choices":[{"message":{"content":"Hi","tool_calls":[{"function":{"name":"f","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}]}`,
			Answer{Text: "Hi", Tools: []ToolCall{{Name: "f", Args: json.RawMessage(`{"a":1}`)}}}},
		{"chat refusal", wire.OpenAIChat, false, `{"choices":[{"message":{"content":null,"refusal":"No."},"finish_reason":"stop"}]}`,
			Answer{Text: "No.", Refused: true}},
		{"messages", wire.AnthropicMessages, false, `{"content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"Hi"},{"type":"tool_use","name":"f","input":{"a":1}}],"stop_reason":"tool_use"}`,
			Answer{Text: "Hi", Tools: []ToolCall{{Name: "f", Args: json.RawMessage(`{"a":1}`)}}}},
		{"chat stream", wire.OpenAIChat, true, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"H\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"i\",\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"f\",\"arguments\":\"{\\\"a\\\"\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\":1}\"}}]}}]}\n\n" +
			"data: [DONE]\n\n",
			Answer{Text: "Hi", Tools: []ToolCall{{Name: "f", Args: json.RawMessage(`{"a":1}`)}}}},
		{"messages stream", wire.AnthropicMessages, true, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"f\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"a\\\":1}\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"}}\n\n",
			Answer{Text: "Hi", Tools: []ToolCall{{Name: "f", Args: json.RawMessage(`{"a":1}`)}}, Refused: true}},
	} {
		got, err := answerOf(tt.f, []byte(tt.body), tt.stream)
		if err != nil || got.Text != tt.want.Text || got.Refused != tt.want.Refused || len(got.Tools) != len(tt.want.Tools) {
			t.Errorf("%s: %+v, %v", tt.name, got, err)
			continue
		}
		for i := range got.Tools {
			if got.Tools[i].Name != tt.want.Tools[i].Name || !sameJSON(got.Tools[i].Args, tt.want.Tools[i].Args) {
				t.Errorf("%s: tool %+v", tt.name, got.Tools[i])
			}
		}
	}
}

func TestUnstreamed(t *testing.T) {
	out, err := unstreamed([]byte(`{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[]}`))
	if err != nil || strings.Contains(string(out), "stream") || !strings.Contains(string(out), `"messages":[]`) {
		t.Fatalf("%s %v", out, err)
	}
	body := []byte(`{"model":"m","messages":[]}`)
	if out, _ := unstreamed(body); string(out) != string(body) {
		t.Fatalf("a whole-answer request changed: %s", out)
	}
}
