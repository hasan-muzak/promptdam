package promptcache

import (
	"sort"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// MarkChat adds cache breakpoints to an OpenAI Chat request that has none
// of its own, as content parts' cache_control (which routers of providers
// such as OpenRouter pass on to the models that cache what a request
// marks, Gemini's and Anthropic's): on the last of the leading system or
// developer messages when the prefix is long enough to cache, and on the
// last message (a user's or a tool's) when the plan has a tail. A string
// content becomes one text part, its JSON string kept as written; an
// array gets the breakpoint on its last part. Only bytes are added: the
// rest of the body is never moved or re-encoded, and nothing is decoded but
// the roles (scan.go). It returns the body unchanged and false when it adds
// nothing.
func MarkChat(body []byte, req wire.Request, p Plan) ([]byte, bool) {
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
	ms, me, ok := member(body, "messages")
	if !ok {
		return body, false
	}
	msgs := elements(body, ms, me)
	if len(msgs) == 0 {
		return body, false
	}
	// Each edit replaces a message's content with the content marked.
	type edit struct {
		start, end int
		with       []byte
	}
	var edits []edit
	mark := func(msg [2]int, cc []byte) {
		cs, ce, ok := member(body[msg[0]:msg[1]], "content")
		if !ok {
			return
		}
		cs, ce = msg[0]+cs, msg[0]+ce
		if marked, ok := markSystem(body[cs:ce], cc); ok {
			edits = append(edits, edit{cs, ce, marked})
		}
	}
	system := -1
	if p.Prefix != "" && estimateTokens(req.Prefix) >= minTokens {
		for i, m := range msgs {
			if role := roleOf(body, m); role != "system" && role != "developer" {
				break
			}
			system = i
		}
		if system >= 0 {
			mark(msgs[system], marker(p.Prefix))
		}
	}
	if last := len(msgs) - 1; p.Tail != "" && last != system {
		if role := roleOf(body, msgs[last]); role == "user" || role == "tool" {
			mark(msgs[last], marker(p.Tail))
		}
	}
	if len(edits) == 0 {
		return body, false
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	size := len(body)
	for _, e := range edits {
		size += len(e.with) - (e.end - e.start)
	}
	out, at := make([]byte, 0, size), 0
	for _, e := range edits {
		out = append(out, body[at:e.start]...)
		out = append(out, e.with...)
		at = e.end
	}
	return append(out, body[at:]...), true
}

// roleOf is the role of the message at doc[span].
func roleOf(doc []byte, span [2]int) string {
	msg := doc[span[0]:span[1]]
	s, e, ok := member(msg, "role")
	if !ok {
		return ""
	}
	return stringValue(msg[s:e])
}
