// Package promptcache turns on Anthropic's prompt caching for requests
// that don't use it: it marks the end of the request's stable prefix (its
// tools, then its system prompt) with cache breakpoints, so the next call
// with the same prefix reads it from the cache at a tenth of the input
// price. It only adds breakpoints: content is never moved or reordered,
// which would change what the model sees.
package promptcache

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/hasan-muzak/keel/pkg/llm/wire"
)

// ephemeral is the breakpoint marker (the five-minute cache).
var ephemeral = json.RawMessage(`{"type":"ephemeral"}`)

// MinTokens is the shortest prefix a model caches: shorter ones aren't
// cached, so they get no breakpoint. Haiku models need more.
func MinTokens(model string) int {
	if strings.Contains(model, "haiku") {
		return 2048
	}
	return 1024
}

// estimateTokens is a prefix's length in tokens, roughly (four bytes a
// token): enough to tell short prefixes from long ones.
func estimateTokens(p wire.Prefix) int { return (len(p.System) + len(p.ToolsRaw)) / 4 }

// Mark adds cache breakpoints at the end of an Anthropic request's tools
// and system prompt, when it has none of its own and its prefix is long
// enough to cache. It returns the body unchanged and false otherwise.
func Mark(body []byte, req wire.Request) ([]byte, bool) {
	p := req.Prefix
	if p.CacheControl || estimateTokens(p) < MinTokens(req.Model) {
		return body, false
	}
	out, marked := body, false
	if raw, ok, err := wire.Field(out, "tools"); err == nil && ok {
		if tools, ok := markLast(raw); ok {
			if next, err := wire.SetField(out, "tools", tools); err == nil {
				out, marked = next, true
			}
		}
	}
	if raw, ok, err := wire.Field(out, "system"); err == nil && ok {
		if system, ok := markSystem(raw); ok {
			if next, err := wire.SetField(out, "system", system); err == nil {
				out, marked = next, true
			}
		}
	}
	return out, marked
}

// markSystem marks a system prompt: a string becomes one text block with a
// breakpoint; an array gets one on its last block.
func markSystem(raw json.RawMessage) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil || text == "" {
			return nil, false
		}
		out, err := json.Marshal([]map[string]any{{"type": "text", "text": text, "cache_control": ephemeral}})
		return out, err == nil
	}
	return markLast(raw)
}

// markLast puts a breakpoint on the last element of an array of objects.
func markLast(raw json.RawMessage) (json.RawMessage, bool) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil, false
	}
	var last map[string]json.RawMessage
	if json.Unmarshal(items[len(items)-1], &last) != nil {
		return nil, false
	}
	last["cache_control"] = ephemeral
	b, err := json.Marshal(last)
	if err != nil {
		return nil, false
	}
	items[len(items)-1] = b
	out, err := json.Marshal(items)
	return out, err == nil
}
