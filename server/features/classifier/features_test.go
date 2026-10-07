package classifier

import (
	"slices"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

func TestFeatures(t *testing.T) {
	in := Input{LastUser: "Is this NDA clause enforceable in Germany?", System: "You are a lawyer.", Family: "f1",
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
	same.LastUser = "is THIS nda clause, enforceable in germany"
	if !slices.Equal(Features(same), fs) {
		t.Fatal("case and punctuation changed the features")
	}
	other := in
	other.LastUser = "How many times is the word revenue used?"
	if slices.Equal(Features(other), fs) {
		t.Fatal("different questions share their features")
	}
	// The text read is bounded.
	long := in
	long.LastUser = strings.Repeat("word ", 10_000)
	if n := len(Features(long)); n > 2*maxUserTokens+maxSystemTokens+16 {
		t.Fatalf("%d features", n)
	}
}

func TestLastUser(t *testing.T) {
	chat := `{"model":"m","messages":[{"role":"system","content":"S"},{"role":"user","content":"first"},
		{"role":"assistant","content":"a"},{"role":"user","content":[{"type":"text","text":"second"},{"type":"image_url","image_url":{"url":"x"}}]}]}`
	if got := LastUser(wire.OpenAIChat, []byte(chat)); got != "second" {
		t.Fatalf("chat: %q", got)
	}
	tool := `{"model":"m","messages":[{"role":"user","content":"q"},{"role":"assistant","tool_calls":[]},{"role":"tool","tool_call_id":"c","content":"42"}]}`
	if got := LastUser(wire.OpenAIChat, []byte(tool)); got != "42" {
		t.Fatalf("a tool result: %q", got)
	}
	messages := `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"rows: 3"}]},{"type":"text","text":"and now?"}]}]}`
	if got := LastUser(wire.AnthropicMessages, []byte(messages)); got != "rows: 3\nand now?" {
		t.Fatalf("messages: %q", got)
	}
}

func BenchmarkFeatures(b *testing.B) {
	in := Input{LastUser: strings.Repeat("Extract the invoice number, total and due date from this text. ", 40),
		System: strings.Repeat("You extract fields from invoices. ", 30), Family: "f", Messages: 2, InputTokens: 2000}
	b.ReportAllocs()
	for b.Loop() {
		Features(in)
	}
}
