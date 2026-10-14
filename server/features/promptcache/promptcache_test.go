package promptcache_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/promptcache"
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

var prefix = promptcache.Plan{Prefix: promptcache.FiveMinutes, MinTokens: 1024}

func TestMark(t *testing.T) {
	body := `{"model":"claude-sonnet-5-5","max_tokens":64,"system":` + mustJSON(long) +
		`,"tools":[{"name":"a","input_schema":{}},  {"name":"b","input_schema":{}} ],"messages":[{"role":"user","content":"hi"}]}`
	out, ok := promptcache.Mark([]byte(body), parse(t, body), prefix)
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
	// Only bytes are added: the client's tools stay as they were written,
	// spacing and key order included.
	if !strings.Contains(string(out), `{"name":"a","input_schema":{}},  {"name":"b","input_schema":{},"cache_control":{"type":"ephemeral"}} ]`) {
		t.Fatalf("the tools were rewritten: %s", out)
	}
	// Breakpoints count as set from now on: marking again changes nothing.
	if again, ok := promptcache.Mark(out, parse(t, string(out)), prefix); ok || string(again) != string(out) {
		t.Fatal("marked twice")
	}

	// A system prompt as blocks gets the breakpoint on its last block.
	blocks := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":[{"type":"text","text":"Rules."},{"type":"text","text":` +
		mustJSON(long) + `}],"messages":[]}`
	out, ok = promptcache.Mark([]byte(blocks), parse(t, blocks), prefix)
	var sys struct {
		System []map[string]any `json:"system"`
	}
	if !ok || json.Unmarshal(out, &sys) != nil || sys.System[0]["cache_control"] != nil || sys.System[1]["cache_control"] == nil {
		t.Fatalf("blocks %s", out)
	}
}

// A conversation's tail gets Anthropic's automatic breakpoint; a one-hour
// tail makes the prefix's an hour too (longer lifetimes come first).
func TestMarkTheTail(t *testing.T) {
	body := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":` + mustJSON(long) + `,"messages":[{"role":"user","content":"a"},
		{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	out, ok := promptcache.Mark([]byte(body), parse(t, body), promptcache.Plan{Prefix: promptcache.FiveMinutes,
		Tail: promptcache.OneHour, MinTokens: 512})
	var got struct {
		CacheControl map[string]any   `json:"cache_control"`
		System       []map[string]any `json:"system"`
	}
	if !ok || json.Unmarshal(out, &got) != nil || got.CacheControl["ttl"] != "1h" || got.System[0]["cache_control"].(map[string]any)["ttl"] != "1h" {
		t.Fatalf("%s", out)
	}
	// A short prefix still gets the tail: the conversation is what grows.
	short := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":"Be brief.","messages":[{"role":"user","content":"a"}]}`
	out, ok = promptcache.Mark([]byte(short), parse(t, short), promptcache.Plan{Prefix: promptcache.FiveMinutes, Tail: promptcache.FiveMinutes})
	if !ok || !strings.Contains(string(out), `"cache_control":{"type":"ephemeral"}}`) || strings.Contains(string(out), `"system":[`) {
		t.Fatalf("short %s", out)
	}
}

func TestMarkLeavesAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		plan promptcache.Plan
	}{
		"short": {`{"model":"claude-sonnet-5-5","max_tokens":9,"system":"Be brief.","messages":[]}`, prefix},
		// Haiku 4.5 caches from 4,096 tokens: 1,800 is too short.
		"short for its model": {`{"model":"claude-haiku-4-5","max_tokens":9,"system":` + mustJSON(long) + `,"messages":[]}`,
			promptcache.Plan{Prefix: promptcache.FiveMinutes, Tail: "", MinTokens: 4096}},
		"own breakpoints": {`{"model":"claude-sonnet-5-5","max_tokens":9,"system":[{"type":"text","text":` + mustJSON(long) +
			`,"cache_control":{"type":"ephemeral"}}],"messages":[]}`, promptcache.Plan{Prefix: promptcache.FiveMinutes, Tail: promptcache.FiveMinutes}},
	} {
		out, ok := promptcache.Mark([]byte(tc.body), parse(t, tc.body), tc.plan)
		if ok || string(out) != tc.body {
			t.Errorf("%s: marked", name)
		}
	}
	// The words "cache_control" in a message aren't a breakpoint.
	words := `{"model":"claude-sonnet-5-5","max_tokens":9,"system":` + mustJSON(long) +
		`,"messages":[{"role":"user","content":"what does \"cache_control\": do?"}]}`
	if _, ok := promptcache.Mark([]byte(words), parse(t, words), prefix); !ok {
		t.Fatal("a message mentioning cache_control turned caching off")
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
