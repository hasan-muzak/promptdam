// Package promptcache turns on prompt caching for requests that don't use
// it, for the models that cache what a request marks: it marks the end of
// the request's stable prefix (its tools, then its system prompt) with
// cache breakpoints, so the next call with the same prefix reads it from
// the cache at a fraction of the input price, and for a conversation its
// tail too. Mark writes Anthropic Messages breakpoints (the tail as
// Anthropic's automatic breakpoint); MarkChat writes OpenAI Chat ones on
// content parts, which routers of providers pass on (OpenRouter's Gemini,
// Anthropic, Alibaba). It only adds bytes: the client's are never moved,
// reordered or re-encoded, which would change what the model sees and what
// the provider's cache keys on.
package promptcache

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// Lifetimes of a breakpoint: Anthropic's default five minutes, or an hour
// (written at twice the input price instead of 1.25 times).
const (
	FiveMinutes = "5m"
	OneHour     = "1h"
)

// Strategies of a call's prompt cache: the gateway adds breakpoints
// (Markers), or the provider caches on its own (Auto); either may also keep
// the call's conversation on one backend of a router of providers
// (WithSession: "markers+session").
const (
	Markers = "markers"
	Auto    = "auto"
)

const sessionSuffix = "+session"

// WithSession is a strategy that also keeps a conversation on one backend.
func WithSession(s string) string { return s + sessionSuffix }

// Marks reports a strategy whose calls get the gateway's breakpoints.
func Marks(s string) bool { return s == Markers || s == Markers+sessionSuffix }

// Sessions reports a strategy that keeps a conversation on one backend.
func Sessions(s string) bool { return strings.HasSuffix(s, sessionSuffix) }

// Base is a strategy without its session.
func Base(s string) string { return strings.TrimSuffix(s, sessionSuffix) }

// DefaultMinTokens is the shortest prefix taken as cacheable when the
// price list doesn't say the model's (512 to 4,096 tokens, by model).
const DefaultMinTokens = 1024

// Plan is how a call's prompt is cached.
type Plan struct {
	// Prefix is the lifetime of the breakpoints on the tools and the
	// system prompt; Tail of the conversation's ("" adds none). A one-hour
	// tail makes the prefix's an hour too: longer lifetimes come first.
	Prefix, Tail string
	// MinTokens is the shortest prefix the target's model caches (0:
	// DefaultMinTokens).
	MinTokens int
}

func marker(ttl string) []byte {
	if ttl == OneHour {
		return []byte(`{"type":"ephemeral","ttl":"1h"}`)
	}
	return []byte(`{"type":"ephemeral"}`)
}

// estimateTokens is a prefix's length in tokens, roughly (four bytes a
// token): enough to tell short prefixes from long ones.
func estimateTokens(p wire.Prefix) int { return (len(p.System) + len(p.ToolsRaw)) / 4 }

// Mark adds cache breakpoints to an Anthropic request that has none of its
// own (any client breakpoint means the client caches as it means to): at
// the end of its tools and of its system prompt when that prefix is long
// enough to cache, and the automatic breakpoint on the conversation's tail
// when the plan has one. It returns the body unchanged and false when it
// adds nothing.
func Mark(body []byte, req wire.Request, p Plan) ([]byte, bool) {
	if req.Prefix.CacheControl {
		return body, false
	}
	if p.Tail == OneHour {
		p.Prefix = OneHour
	}
	minTokens := p.MinTokens
	if minTokens <= 0 {
		minTokens = DefaultMinTokens
	}
	out, marked := body, false
	if p.Prefix != "" && estimateTokens(req.Prefix) >= minTokens {
		cc := marker(p.Prefix)
		if raw, ok, err := wire.Field(out, "tools"); err == nil && ok {
			if tools, ok := markLast(raw, cc); ok {
				if next, err := wire.SetField(out, "tools", tools); err == nil {
					out, marked = next, true
				}
			}
		}
		if raw, ok, err := wire.Field(out, "system"); err == nil && ok {
			if system, ok := markSystem(raw, cc); ok {
				if next, err := wire.SetField(out, "system", system); err == nil {
					out, marked = next, true
				}
			}
		}
	}
	if p.Tail != "" {
		if next, err := wire.SetField(out, "cache_control", marker(p.Tail)); err == nil {
			out, marked = next, true
		}
	}
	return out, marked
}

// markSystem marks a system prompt: a string becomes one text block (its
// JSON string kept as written) with a breakpoint; an array gets one on its
// last block.
func markSystem(raw json.RawMessage, cc []byte) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	if raw[0] == '"' {
		if len(raw) <= 2 {
			return nil, false // an empty prompt caches nothing
		}
		out := make([]byte, 0, len(raw)+len(cc)+48)
		out = append(out, `[{"type":"text","text":`...)
		out = append(out, raw...)
		out = append(out, `,"cache_control":`...)
		out = append(out, cc...)
		return append(out, "}]"...), true
	}
	return markLast(raw, cc)
}

// markLast puts a breakpoint on the last element of an array of objects,
// inserting it before the element's closing brace: every other byte stays.
func markLast(raw json.RawMessage, cc []byte) (json.RawMessage, bool) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil, false
	}
	last := bytes.TrimSpace(items[len(items)-1])
	if len(last) < 2 || last[0] != '{' || last[len(last)-1] != '}' {
		return nil, false
	}
	at := bytes.LastIndex(raw, last)
	if at < 0 {
		return nil, false
	}
	brace := at + len(last) - 1
	member := append([]byte(`"cache_control":`), cc...)
	if len(bytes.TrimSpace(last[1:len(last)-1])) > 0 {
		member = append([]byte{','}, member...)
	}
	out := make([]byte, 0, len(raw)+len(member))
	out = append(out, raw[:brace]...)
	out = append(out, member...)
	return append(out, raw[brace:]...), true
}
