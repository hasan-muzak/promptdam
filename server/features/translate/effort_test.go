package translate

import (
	"encoding/json"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

func TestFitEffort(t *testing.T) {
	all := []string{"low", "medium", "high", "xhigh", "max"}
	noXHigh := []string{"low", "medium", "high", "max"}
	tests := []struct {
		want    string
		efforts []string
		known   bool
		f       wire.Format
		fit     string
	}{
		{"medium", all, true, wire.AnthropicMessages, "medium"},
		{"xhigh", noXHigh, true, wire.AnthropicMessages, "max"}, // a tie goes to the stronger
		{"minimal", all, true, wire.AnthropicMessages, "low"},   // the nearest
		{"low", []string{}, true, wire.AnthropicMessages, ""},   // a model that takes none
		{"none", nil, false, wire.AnthropicMessages, "low"},     // Anthropic's levels start at low
		{"xhigh", nil, false, wire.OpenAIChat, "high"},          // OpenAI-compatible: low to high
		{"minimal", nil, false, wire.OpenAIChat, "low"},
		{"", all, true, wire.AnthropicMessages, ""},
	}
	for _, tt := range tests {
		if got := FitEffort(tt.want, tt.efforts, tt.known, tt.f); got != tt.fit {
			t.Errorf("FitEffort(%q, %v, %v) = %q, want %q", tt.want, tt.efforts, tt.known, got, tt.fit)
		}
	}
	if ValidEffort("huge") || !ValidEffort("") || !ValidEffort("xhigh") {
		t.Fatal("ValidEffort")
	}
}

func TestSetEffort(t *testing.T) {
	field := func(t *testing.T, body []byte, path ...string) string {
		t.Helper()
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		for _, p := range path {
			m, ok := v.(map[string]any)
			if !ok {
				return ""
			}
			v = m[p]
		}
		s, _ := v.(string)
		return s
	}
	// Anthropic: output_config.effort, beside a structured output's format.
	msgs := []byte(`{"model":"claude-opus-5-5","output_config":{"format":{"type":"json_schema","schema":{}}},"messages":[]}`)
	out, ok, err := SetEffort(msgs, wire.AnthropicMessages, "anthropic", "medium")
	if err != nil || !ok || field(t, out, "output_config", "effort") != "medium" || field(t, out, "output_config", "format", "type") != "json_schema" {
		t.Fatalf("anthropic: %s %v %v", out, ok, err)
	}
	if !HasEffort(out, wire.AnthropicMessages, "anthropic") || HasEffort(msgs, wire.AnthropicMessages, "anthropic") {
		t.Fatal("HasEffort, anthropic")
	}
	chat := []byte(`{"model":"gpt-5","messages":[]}`)
	if out, ok, _ := SetEffort(chat, wire.OpenAIChat, "openai", "low"); !ok || field(t, out, "reasoning_effort") != "low" {
		t.Fatalf("openai: %s", out)
	}
	if out, ok, _ := SetEffort(chat, wire.OpenAIChat, "openrouter", "high"); !ok || field(t, out, "reasoning", "effort") != "high" {
		t.Fatalf("openrouter: %s", out)
	}
	if out, ok, _ := SetEffort(chat, wire.OpenAIChat, "mistral", "high"); ok || string(out) != string(chat) {
		t.Fatalf("a provider without a known dialect: %s", out)
	}
	// A model known not to reason loses the client's effort.
	withEffort := []byte(`{"model":"gpt-4.1","reasoning_effort":"high","messages":[]}`)
	if out, ok := StripEffort(withEffort); !ok || HasEffort(out, wire.OpenAIChat, "openai") || field(t, out, "model") != "gpt-4.1" {
		t.Fatalf("strip: %s", out)
	}
}
