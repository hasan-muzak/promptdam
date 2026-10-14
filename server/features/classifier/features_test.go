package classifier

import (
	"slices"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

func TestFeatures(t *testing.T) {
	in := Input{Ask: "Is this NDA clause enforceable in Germany?", System: "You are a lawyer.", Family: "f1",
		Tags: []string{"legal"}, InputTokens: 300, Messages: 2, Tools: true}
	fs := Features(in)
	if !slices.IsSorted(fs) || len(slices.Compact(slices.Clone(fs))) != len(fs) {
		t.Fatalf("not sorted and unique: %v", fs)
	}
	for _, f := range fs {
		if f >= Dim {
			t.Fatalf("bucket %d out of range", f)
		}
	}
	if !slices.Equal(fs, Features(in)) {
		t.Fatal("features change between runs")
	}
	// Words, not their case or punctuation, make a feature.
	same := in
	same.Ask = "is THIS nda clause, enforceable in germany"
	if !slices.Equal(Features(same), fs) {
		t.Fatal("case and punctuation changed the features")
	}
	other := in
	other.Ask = "How many times is the word revenue used?"
	if slices.Equal(Features(other), fs) {
		t.Fatal("different questions share their features")
	}
	// The text read is bounded.
	long := in
	long.Ask = strings.Repeat("word ", 10_000)
	if n := len(Features(long)); n > 2*maxUserTokens+maxSystemTokens+16 {
		t.Fatalf("%d features", n)
	}
}

// A call's input is its turn's: every call of an agent's tool loop reads
// as the question that started it, never the tool results after it.
func TestInputOfReadsTheTurn(t *testing.T) {
	parse := func(f wire.Format, body string) wire.Request {
		t.Helper()
		r, err := wire.ParseRequest(f, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	start := parse(wire.AnthropicMessages, `{"model":"m","max_tokens":9,"system":"S","messages":[
		{"role":"user","content":[{"type":"text","text":"why does the build fail?"}]}]}`)
	loop := parse(wire.AnthropicMessages, `{"model":"m","max_tokens":9,"system":"S","messages":[
		{"role":"user","content":[{"type":"text","text":"why does the build fail?"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"sh","input":{"cmd":"go build"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"undefined: x"},{"type":"text","text":"<reminder/>"}]}]}`)
	a, b := InputOf(start, "f", nil), InputOf(loop, "f", nil)
	if a.Ask != "why does the build fail?" || !slices.Equal(Features(a), Features(b)) {
		t.Fatalf("the tool loop's input %+v differs from its turn's %+v", b, a)
	}
	chat := parse(wire.OpenAIChat, `{"model":"m","messages":[{"role":"system","content":"S"},{"role":"user","content":"first"},
		{"role":"assistant","content":"a"},{"role":"user","content":[{"type":"text","text":"second"},{"type":"image_url","image_url":{"url":"x"}}]}]}`)
	if in := InputOf(chat, "f", nil); in.Ask != "second" || !in.Images || in.Messages != 4 {
		t.Fatalf("chat %+v", in)
	}
}
