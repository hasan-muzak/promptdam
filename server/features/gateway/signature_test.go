package gateway_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// A conversation moved to Gemini 3 mid-turn has tool calls no Gemini
// signed: the first call of each step in the current turn gets Google's
// skip value, signed steps and earlier turns keep theirs, and other
// models get the call as it was.
func TestGeminiSignatures(t *testing.T) {
	conversation := `{"model":"advisor","messages":[
		{"role":"user","content":"rate of 8471?"},
		{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"rate","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"a","content":"0%"},
		{"role":"user","content":"and of 8517?"},
		{"role":"assistant","content":"Checking.","tool_calls":[{"id":"b","type":"function","function":{"name":"rate","arguments":"{}"}},
			{"id":"c","type":"function","function":{"name":"rate","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"b","content":"0%"},{"role":"tool","tool_call_id":"c","content":"0%"},
		{"role":"assistant","tool_calls":[{"id":"d","type":"function","function":{"name":"rate","arguments":"{}"},
			"extra_content":{"google":{"thought_signature":"c2ln"}}}]},
		{"role":"tool","tool_call_id":"d","content":"0%"}]}`
	type sent struct {
		Messages []struct {
			ToolCalls []struct {
				ID    string `json:"id"`
				Extra *struct {
					Google struct {
						ThoughtSignature string `json:"thought_signature"`
					} `json:"google"`
				} `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	signatures := func(model, kind string) string {
		e := setup(t, fakeupstream.Options{}, func(base string) gateway.Resolver {
			return gateway.Static{{Provider: kind, Kind: kind, BaseURL: base + "/v1", Model: model}}
		})
		if res, body := e.post(t, "/v1/chat/completions", conversation); res.StatusCode != 200 {
			t.Fatalf("%d %s", res.StatusCode, body)
		}
		var s sent
		if err := json.Unmarshal(e.fake.Requests()[0].Body, &s); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range s.Messages {
			for _, tc := range m.ToolCalls {
				sig := "-"
				if tc.Extra != nil {
					sig = tc.Extra.Google.ThoughtSignature
				}
				got = append(got, tc.ID+"="+sig)
			}
		}
		return strings.Join(got, " ")
	}
	if got := signatures("gemini-3.5-flash", "gemini"); got != "a=- b=skip_thought_signature_validator c=- d=c2ln" {
		t.Fatalf("gemini 3: %s", got)
	}
	if got := signatures("google/gemini-flash-latest", "vertex"); got != "a=- b=skip_thought_signature_validator c=- d=c2ln" {
		t.Fatalf("vertex: %s", got)
	}
	for _, c := range [][2]string{{"gemini-2.5-flash", "gemini"}, {"gpt-5-mini", "openai"}, {"google/gemini-3.5-flash", "openrouter"}} {
		if got := signatures(c[0], c[1]); got != "a=- b=- c=- d=c2ln" {
			t.Fatalf("%s on %s: %s", c[0], c[1], got)
		}
	}
}
