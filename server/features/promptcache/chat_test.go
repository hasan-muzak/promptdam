package promptcache_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/promptcache"
)

func parseChat(t *testing.T, body string) wire.Request {
	t.Helper()
	r, err := wire.ParseRequest(wire.OpenAIChat, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// content is message i's content, decoded.
func content(t *testing.T, body []byte, i int) any {
	t.Helper()
	var b struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return b.Messages[i].Content
}

func markedPart(t *testing.T, c any) string {
	t.Helper()
	parts, ok := c.([]any)
	if !ok || len(parts) == 0 {
		return ""
	}
	last, _ := parts[len(parts)-1].(map[string]any)
	cc, _ := last["cache_control"].(map[string]any)
	if cc == nil {
		return ""
	}
	if ttl, _ := cc["ttl"].(string); ttl != "" {
		return ttl
	}
	return "5m"
}

// A Chat prompt gets a breakpoint on its system prompt (the last of the
// leading system messages) and, for a conversation, on its last message
// and where the previous call's last breakpoint stood; a string content
// becomes one text part with its JSON string as written, and every byte
// outside the marked contents stays.
func TestMarkChat(t *testing.T) {
	system := strings.Repeat("You review contracts. ", 300)
	body := `{"model":"google/gemini-3.8-flash","temperature":0.2,"messages":[` +
		`{"role":"system","content":"Be brief."},` +
		`{"role":"developer","content":"` + system + `"},` +
		`{"role":"user","content":"Is clause 4 \"enforceable\"?"},` +
		`{"role":"assistant","content":"Probably."},` +
		`{"role":"user","content":[{"type":"text","text":"Why?"}]}],"stream":true}`
	out, ok := promptcache.MarkChat([]byte(body), parseChat(t, body), promptcache.Plan{Prefix: promptcache.FiveMinutes,
		Tail: promptcache.FiveMinutes})
	if !ok {
		t.Fatal("nothing marked")
	}
	if markedPart(t, content(t, out, 0)) != "" || markedPart(t, content(t, out, 1)) != "5m" || markedPart(t, content(t, out, 2)) != "5m" ||
		markedPart(t, content(t, out, 4)) != "5m" {
		t.Fatalf("marked %s", out)
	}
	if c, _ := content(t, out, 3).(string); c != "Probably." {
		t.Fatalf("an unmarked message changed: %s", out)
	}
	if !strings.Contains(string(out), `{"type":"text","text":"Is clause 4 \"enforceable\"?","cache_control":{"type":"ephemeral"}}`) {
		t.Fatalf("the previous call's last message lost its bytes: %s", out)
	}
	if n := strings.Count(string(out), "cache_control"); n != 3 {
		t.Fatalf("%d breakpoints", n)
	}
	// The system prompt keeps its bytes: only the part's wrapping is added.
	if !strings.Contains(string(out), `[{"type":"text","text":"`+system+`","cache_control":{"type":"ephemeral"}}]`) {
		t.Fatalf("the system prompt's bytes moved: %s", out)
	}
	// Everything before the first change and after the last is the same.
	head := strings.Index(body, `{"role":"developer","content":`) + len(`{"role":"developer","content":`)
	if string(out[:head]) != body[:head] || !strings.HasSuffix(string(out), `],"stream":true}`) {
		t.Fatalf("bytes outside the marked contents moved: %s", out)
	}

	// An hour's tail makes the prefix's an hour too.
	hour, _ := promptcache.MarkChat([]byte(body), parseChat(t, body), promptcache.Plan{Prefix: promptcache.FiveMinutes,
		Tail: promptcache.OneHour})
	if markedPart(t, content(t, hour, 1)) != "1h" || markedPart(t, content(t, hour, 4)) != "1h" {
		t.Fatalf("one hour: %s", hour)
	}
}

// What MarkChat leaves alone: a client's own breakpoints, a prefix too
// short to cache (the tail still marked), a last message that isn't a
// user's or a tool's, empty contents, and bodies without messages.
func TestMarkChatLeavesAlone(t *testing.T) {
	plan := promptcache.Plan{Prefix: promptcache.FiveMinutes, Tail: promptcache.FiveMinutes}
	own := `{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"user","content":"hi"}]}`
	if out, ok := promptcache.MarkChat([]byte(own), parseChat(t, own), plan); ok || string(out) != own {
		t.Fatalf("a client's breakpoints: %s", out)
	}

	short := `{"model":"m","messages":[{"role":"system","content":"Short."},{"role":"user","content":"hi"}]}`
	out, ok := promptcache.MarkChat([]byte(short), parseChat(t, short), plan)
	if !ok || markedPart(t, content(t, out, 0)) != "" || markedPart(t, content(t, out, 1)) != "5m" {
		t.Fatalf("a short prefix: %s", out)
	}

	prefill := `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"Hel"}]}`
	if out, ok := promptcache.MarkChat([]byte(prefill), parseChat(t, prefill), plan); ok || string(out) != prefill {
		t.Fatalf("an assistant's last message: %s", out)
	}

	tool := `{"model":"m","messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":null,` +
		`"tool_calls":[{"id":"c1","type":"function","function":{"name":"w","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"18 C"}]}`
	if out, ok := promptcache.MarkChat([]byte(tool), parseChat(t, tool), plan); !ok || markedPart(t, content(t, out, 2)) != "5m" {
		t.Fatalf("a tool's result: %s", out)
	}

	empty := `{"model":"m","messages":[{"role":"user","content":""}]}`
	if out, ok := promptcache.MarkChat([]byte(empty), parseChat(t, empty), plan); ok || string(out) != empty {
		t.Fatalf("an empty content: %s", out)
	}
	if _, ok := promptcache.MarkChat([]byte(`{"model":"m","messages":[]}`), wire.Request{}, plan); ok {
		t.Fatal("no messages")
	}
}

// The previous call's last message keeps its breakpoint: in an agent's
// tool loop, the result before the assistant's last tool call (or the
// user's message, when the loop just began); on a conversation's first
// call there is none.
func TestMarkChatKeepsThePreviousBreakpoint(t *testing.T) {
	plan := promptcache.Plan{Prefix: promptcache.FiveMinutes, Tail: promptcache.FiveMinutes}
	call := func(name string) string {
		return `{"role":"assistant","content":null,"tool_calls":[{"id":"` + name + `","type":"function","function":{"name":"ls","arguments":"{}"}}]}`
	}
	result := func(name, text string) string {
		return `{"role":"tool","tool_call_id":"` + name + `","content":"` + text + `"}`
	}
	marked := func(body string) []int {
		t.Helper()
		out, _ := promptcache.MarkChat([]byte(body), parseChat(t, body), plan)
		var b struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(out, &b); err != nil {
			t.Fatal(err)
		}
		var at []int
		for i := range b.Messages {
			if markedPart(t, content(t, out, i)) != "" {
				at = append(at, i)
			}
		}
		return at
	}
	for _, tt := range []struct {
		name, body string
		want       []int
	}{
		{"a first call", `{"model":"m","messages":[{"role":"user","content":"list the files"}]}`, []int{0}},
		{"a loop just begun", `{"model":"m","messages":[{"role":"user","content":"list the files"},` + call("c1") + `,` +
			result("c1", "a.go") + `]}`, []int{0, 2}},
		{"parallel results", `{"model":"m","messages":[{"role":"user","content":"list the files"},` + call("c1") + `,` +
			result("c1", "a.go") + `,` + call("c2") + `,` + result("c2", "b.go") + `,` + result("c2", "c.go") + `]}`, []int{2, 5}},
		{"a new question after a loop", `{"model":"m","messages":[{"role":"user","content":"list the files"},` + call("c1") + `,` +
			result("c1", "a.go") + `,{"role":"assistant","content":"a.go"},{"role":"user","content":"and tests?"}]}`, []int{2, 4}},
	} {
		if got := marked(tt.body); !slices.Equal(got, tt.want) {
			t.Errorf("%s: marked %v, want %v", tt.name, got, tt.want)
		}
	}
}

func BenchmarkMarkChat(b *testing.B) {
	system := strings.Repeat("You review contracts and cite the clauses. ", 200)
	var msgs []string
	for i := range 40 {
		msgs = append(msgs, `{"role":"user","content":"Question `+strings.Repeat("x", i)+`"}`, `{"role":"assistant","content":"Answer."}`)
	}
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"` + system + `"},` + strings.Join(msgs, ",") +
		`,{"role":"user","content":"Last?"}]}`)
	req, _ := wire.ParseRequest(wire.OpenAIChat, body)
	plan := promptcache.Plan{Prefix: promptcache.FiveMinutes, Tail: promptcache.FiveMinutes}
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		promptcache.MarkChat(body, req, plan)
	}
}
