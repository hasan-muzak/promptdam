package firewall

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

const (
	adaMail  = "ada@example.com"
	adaPhone = "+44 20 7946 0958"
)

// escape is a token as a JSON document inside a string escapes it.
func escape(tok string) string {
	esc := func(code string) string { return string(rune(0x5c)) + "u" + code }
	return esc("27e6") + strings.TrimSuffix(strings.TrimPrefix(tok, "⟦"), "⟧") + esc("27e7")
}

// An object's own index field goes into its path: OpenAI's parallel tool
// calls sit at one array position, Anthropic's blocks at the top level.
func TestPathsKeepToolCallsApart(t *testing.T) {
	paths := func(doc string) []string {
		var out []string
		if _, _, err := rewrite([]byte(doc), func(w *Walker, s string) (string, bool) {
			if w.Key() != "type" {
				out = append(out, w.Path())
			}
			return "", false
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	a := paths(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"x"}}]}}]}`)
	b := paths(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"y"}}]}}]}`)
	if len(a) != 1 || len(b) != 1 || a[0] == b[0] || !strings.HasSuffix(b[0], "function#1.arguments") {
		t.Fatalf("openai %v %v", a, b)
	}
	c := paths(`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"z"}}`)
	if len(c) != 1 || c[0] != "delta#2.partial_json" {
		t.Fatalf("anthropic %v", c)
	}
}

// Inside a tool's input, id, type and the like are the tool's own fields:
// they are read, masked and restored. The protocol's identifiers stay
// unread, the Responses API's top-level input among them.
func TestToolInputIsRead(t *testing.T) {
	e := engine(t, func(s *Settings) { s.PII = Tokenize })
	body := []byte(`{"model":"claude","max_tokens":5,"messages":[{"role":"assistant","content":[{"type":"tool_use",` +
		`"id":"toolu_1","name":"crm","input":{"id":"` + adaMail + `","type":"` + adaPhone + `"}}]}]}`)
	o, err := e.Inspect(body)
	if err != nil || o.Body == nil || strings.Contains(string(o.Body), adaMail) || strings.Contains(string(o.Body), adaPhone) ||
		!strings.Contains(string(o.Body), `"id":"toolu_1"`) {
		t.Fatalf("tool input %s %v", o.Body, err)
	}
	mail := o.Tokens.Token("email", adaMail)
	answer := `{"content":[{"type":"tool_use","id":"toolu_2","name":"crm","input":{"id":"` + mail + `"}}]}`
	if got := string(e.Response(o).Body([]byte(answer))); !strings.Contains(got, `"id":"`+adaMail+`"`) {
		t.Fatalf("restored %s", got)
	}

	// A key in a tool's input is blocked like any other.
	key := []byte(`{"model":"claude","max_tokens":5,"messages":[{"role":"assistant","content":[{"type":"tool_use",` +
		`"id":"toolu_1","name":"deploy","input":{"type":"` + awsKey + `"}}]}]}`)
	if o, _ := engine(t, nil).Inspect(key); !o.Blocked {
		t.Fatalf("key in tool input: %+v", o)
	}
	// The Responses API's call ids aren't data.
	responses := []byte(`{"model":"gpt-5","input":[{"type":"function_call_output","call_id":"` + adaMail + `","output":"ok"}]}`)
	if o, _ := e.Inspect(responses); o.Body != nil || len(o.Hits) != 0 {
		t.Fatalf("call id read: %s %+v", o.Body, o.Hits)
	}
}

// A model may write tokens in a tool call's arguments, escaped as JSON
// inside JSON: they are restored there too.
func TestToolCallArgumentsRestored(t *testing.T) {
	e := engine(t, func(s *Settings) { s.PII = Tokenize })
	o, _ := e.Inspect(chat("Mail " + adaMail))
	mail := o.Tokens.Token("email", adaMail)
	args, _ := json.Marshal(`{"to":"` + escape(mail) + `","cc":"` + mail + `"}`)
	answer := `{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"send",` +
		`"arguments":` + string(args) + `}}]}}]}`
	var got struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct{ Arguments string } `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(e.Response(o).Body([]byte(answer)), &got); err != nil {
		t.Fatal(err)
	}
	if a := got.Choices[0].Message.ToolCalls[0].Function.Arguments; a != `{"to":"`+adaMail+`","cc":"`+adaMail+`"}` {
		t.Fatalf("arguments %s", a)
	}
}

// Streamed tool calls: two of OpenAI's in parallel and one of Anthropic's,
// each with a token cut between events, come back whole and apart.
func TestStreamedToolCallsRestored(t *testing.T) {
	e := engine(t, func(s *Settings) { s.PII = Tokenize })
	o, _ := e.Inspect(chat("Mail " + adaMail + ", call " + adaPhone))
	mail, phone := o.Tokens.Token("email", adaMail), o.Tokens.Token("phone", adaPhone)

	r := e.Response(o)
	args := map[float64]string{}
	cutA, cutB := len(mail)/2, len(phone)/2
	pieces := []struct {
		index int
		s     string
	}{
		{0, `{"to":"` + mail[:cutA]}, {1, `{"phone":"` + phone[:cutB]}, {0, mail[cutA:] + `"}`}, {1, phone[cutB:] + `"}`},
	}
	for _, p := range pieces {
		// As OpenAI writes them: each object's index before its content.
		arg, _ := json.Marshal(p.s)
		ev := []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":` + strconv.Itoa(p.index) +
			`,"function":{"arguments":` + string(arg) + `}}]}}]}`)
		if out, _ := r.Event("", ev); out != nil {
			ev = out
		}
		var c struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index    float64
						Function struct{ Arguments string } `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(ev, &c)
		tc := c.Choices[0].Delta.ToolCalls[0]
		args[tc.Index] += tc.Function.Arguments
	}
	if args[0] != `{"to":"`+adaMail+`"}` || args[1] != `{"phone":"`+adaPhone+`"}` {
		t.Fatalf("openai %v", args)
	}

	r = e.Response(o)
	var input strings.Builder
	esc := escape(mail)
	for _, piece := range []string{`{"to":"` + esc[:4], esc[4:10], esc[10:] + `"}`} {
		part, _ := json.Marshal(piece)
		ev := []byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":` +
			string(part) + `}}`)
		if out, _ := r.Event("", ev); out != nil {
			ev = out
		}
		var d struct {
			Delta struct {
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		_ = json.Unmarshal(ev, &d)
		input.WriteString(d.Delta.PartialJSON)
	}
	if input.String() != `{"to":"`+adaMail+`"}` {
		t.Fatalf("anthropic %q", input.String())
	}
}

// A stream that ends inside what looked like a token loses nothing: the
// held text goes out in a copy of its block's last event, before the event
// that ends the block, or when the stream ends.
func TestHeldTextIsReleased(t *testing.T) {
	e := engine(t, func(s *Settings) { s.PII = Tokenize })
	o, _ := e.Inspect(chat("Mail " + adaMail))

	r := e.Response(o)
	delta := func(text string) []byte {
		b, _ := json.Marshal(text)
		return []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(b) + `}}`)
	}
	text := func(data []byte) string {
		var d struct {
			Delta struct{ Text string } `json:"delta"`
		}
		_ = json.Unmarshal(data, &d)
		return d.Delta.Text
	}
	var got strings.Builder
	out, before := r.Event("content_block_delta", delta("Price: 5 ⟦eur"))
	if before != nil || text(out) != "Price: 5 " {
		t.Fatalf("held: %q %v", text(out), before)
	}
	got.WriteString(text(out))
	_, before = r.Event("content_block_stop", []byte(`{"type":"content_block_stop","index":0}`))
	if len(before) != 1 || before[0].Name != "content_block_delta" || text(before[0].Data) != "⟦eur" {
		t.Fatalf("released at the block's end: %+v", before)
	}
	if got.WriteString(text(before[0].Data)); got.String() != "Price: 5 ⟦eur" {
		t.Fatalf("text %q", got.String())
	}
	if r.End() != nil {
		t.Fatal("released twice")
	}

	// A stream cut with no block end: End releases it.
	r = e.Response(o)
	choice := func(content string) []byte {
		b, _ := json.Marshal(content)
		return []byte(`{"id":"c1","choices":[{"index":0,"delta":{"content":` + string(b) + `}}]}`)
	}
	r.Event("", choice("Mail ⟦em"))
	rest := r.End()
	var c struct {
		ID      string
		Choices []struct {
			Delta struct{ Content string } `json:"delta"`
		} `json:"choices"`
	}
	if len(rest) != 1 || json.Unmarshal(rest[0].Data, &c) != nil || c.ID != "c1" || c.Choices[0].Delta.Content != "⟦em" {
		t.Fatalf("released at the end: %+v", rest)
	}
}

// With the gateway's key, a value's token is named by its conversation and
// itself: the next call of the conversation tokenizes it alike even when a
// new value comes before it, and another conversation's token differs.
func TestTokensNamedByConversation(t *testing.T) {
	e := engine(t, func(s *Settings) { s.PII = Tokenize })
	key := []byte("0123456789abcdef0123456789abcdef")
	conv := Scan{Conversation: wire.Anchor{1}, Key: key}
	first, _ := e.InspectCall(chat("Mail "+adaMail), conv)
	later := []byte(`{"model":"claude","max_tokens":5,"system":"Copy ` + adaPhone + `","messages":[` +
		`{"role":"user","content":"Mail ` + adaMail + `"}]}`)
	second, _ := e.InspectCall(later, conv)
	tok := first.Tokens.Token("email", adaMail)
	if !strings.HasPrefix(tok, "⟦email_") || second.Tokens.Token("email", adaMail) != tok ||
		!strings.Contains(string(second.Body), tok) {
		t.Fatalf("tokens differ: %s, %s", tok, second.Body)
	}
	other, _ := e.InspectCall(chat("Mail "+adaMail), Scan{Conversation: wire.Anchor{2}, Key: key})
	if other.Tokens.Token("email", adaMail) == tok {
		t.Fatal("linkable across conversations")
	}
	// The answer gets the value back as always.
	answer := `{"choices":[{"message":{"content":"Sent to ` + tok + `"}}]}`
	if got := string(e.Response(second).Body([]byte(answer))); !strings.Contains(got, adaMail) {
		t.Fatalf("restored %s", got)
	}
}
