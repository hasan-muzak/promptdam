package promptcache_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel/pkg/llm/wire"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/promptcache"
)

func parse(t *testing.T, body string) wire.Request {
	t.Helper()
	r, err := wire.ParseRequest(wire.AnthropicMessages, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var long = strings.Repeat("Follow the house style. ", 300) // about 1,800 tokens

func TestMark(t *testing.T) {
	body := `{"model":"claude-sonnet-5-5","max_tokens":64,"system":` + mustJSON(long) +
		`,"tools":[{"name":"a","input_schema":{}},{"name":"b","input_schema":{}}],"messages":[{"role":"user","content":"hi"}]}`
	out, ok := promptcache.Mark([]byte(body), parse(t, body))
	if !ok {
		t.Fatal("not marked")
	}
	var got struct {
		System []map[string]any `json:"system"`
		Tools  []map[string]any `json:"tools"`
		Model  string           `json:"model"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.System) != 1 || got.System[0]["text"] != long || got.System[0]["cache_control"] == nil {
		t.Fatalf("system %v", got.System)
	}
	if got.Tools[0]["cache_control"] != nil || got.Tools[1]["cache_control"] == nil || got.Model != "claude-sonnet-5-5" {
		t.Fatalf("tools %v", got.Tools)
	}
	// Breakpoints count as set from now on: marking again changes nothing.
	if again, ok := promptcache.Mark(out, parse(t, string(out))); ok || string(again) != string(out) {
		t.Fatal("marked twice")
	}

	// A system prompt as blocks gets the breakpoint on its last block.
	blocks := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":[{"type":"text","text":"Rules."},{"type":"text","text":` +
		mustJSON(long) + `}],"messages":[]}`
	out, ok = promptcache.Mark([]byte(blocks), parse(t, blocks))
	var sys struct {
		System []map[string]any `json:"system"`
	}
	if !ok || json.Unmarshal(out, &sys) != nil || sys.System[0]["cache_control"] != nil || sys.System[1]["cache_control"] == nil {
		t.Fatalf("blocks %s", out)
	}
}

func TestMarkLeavesAlone(t *testing.T) {
	for name, body := range map[string]string{
		"short": `{"model":"claude-sonnet-5-5","max_tokens":9,"system":"Be brief.","messages":[]}`,
		// Haiku caches from about 2,048 tokens: 1,800 is too short.
		"short for haiku": `{"model":"claude-haiku-4-5","max_tokens":9,"system":` + mustJSON(long) + `,"messages":[]}`,
		"own breakpoints": `{"model":"claude-sonnet-5-5","max_tokens":9,"system":[{"type":"text","text":` + mustJSON(long) +
			`,"cache_control":{"type":"ephemeral"}}],"messages":[]}`,
	} {
		out, ok := promptcache.Mark([]byte(body), parse(t, body))
		if ok || string(out) != body {
			t.Errorf("%s: marked", name)
		}
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
