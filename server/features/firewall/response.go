package firewall

import (
	"bytes"
	"maps"
	"slices"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/redact"
)

// Response filters a call's answer: it puts the request's tokenized values
// back, in a whole answer or in a stream's events, and scans the answer
// for findings. Findings in an answer only alert: what was written can't
// be taken back. It is not safe for concurrent use.
type Response struct {
	e      *Engine
	tokens *redact.Tokenizer
	// restorers hold, per place in a stream's events (choices.0.delta
	// .content…), a token cut between two events.
	restorers map[string]*redact.Restorer
	// last is, per place, the last event a restorer wrote to: held text
	// is released in a copy of it when its block ends.
	last map[string]wire.Event
	// scans read a stream's text per place too: pieces of one value
	// arrive at one place, between the other places' strings.
	scan  bool
	scans map[string]*redact.Stream
	hits  map[string]*Hit
	order []string
}

// Response returns the filter of a call's answer; nil when it has nothing
// to do (no tokens, answers not scanned).
func (e *Engine) Response(o *Outcome) *Response {
	var tokens *redact.Tokenizer
	if o != nil && o.Tokens != nil && o.Tokens.Len() > 0 {
		tokens = o.Tokens
	}
	if tokens == nil && !e.s.ScanResponses {
		return nil
	}
	return &Response{e: e, tokens: tokens, scan: e.s.ScanResponses, hits: map[string]*Hit{}}
}

// maybeToken reports a string that may hold a token, as it is or
// JSON-escaped (a model's tool call arguments), or that ends where one may
// begin (a stream's next piece completes it).
func maybeToken(s string) bool { return redact.MayHoldToken(s) }

// Body filters a whole answer; nil when it is unchanged.
func (r *Response) Body(doc []byte) []byte {
	out, changed, err := rewrite(doc, func(w *Walker, s string) (string, bool) {
		if ignored(w) {
			return "", false
		}
		if r.scan {
			r.found(r.e.find(s))
		}
		if r.tokens != nil && maybeToken(s) {
			if restored := r.tokens.Restore(s); restored != s {
				return restored, true
			}
		}
		return "", false
	})
	if err != nil || !changed {
		return nil
	}
	return out
}

// Event filters one event of a streamed answer (its name and data): its
// data, nil when unchanged, and the events to write before it. Those carry
// text held back from earlier events, released when a block or the
// answer ends here (a stream cut inside what looked like a token).
func (r *Response) Event(name string, data []byte) (out []byte, before []wire.Event) {
	if len(r.last) > 0 && ends(data) {
		before = r.release()
	}
	return r.event(name, data), before
}

// ends reports an event that ends a block or the answer: Anthropic's
// content_block_stop and message_stop, a Chat Completions choice's
// finish_reason, the Responses API's *.done and response.completed.
func ends(data []byte) bool {
	return bytes.Contains(data, []byte(`"content_block_stop"`)) || bytes.Contains(data, []byte(`"message_stop"`)) ||
		bytes.Contains(data, []byte(`.done"`)) || bytes.Contains(data, []byte(`"response.completed"`)) ||
		bytes.Contains(data, []byte(`"finish_reason":"`))
}

// release returns, per place still holding text, a copy of its last event
// carrying only that text.
func (r *Response) release() []wire.Event {
	var out []wire.Event
	for _, path := range slices.Sorted(maps.Keys(r.last)) {
		last := r.last[path]
		delete(r.last, path)
		text := r.restorers[path].Flush()
		if text == "" {
			continue
		}
		data, _, err := rewrite(last.Data, func(w *Walker, s string) (string, bool) {
			switch {
			case w.Path() == path:
				return text, true
			case ignored(w) || s == "":
				return "", false
			}
			return "", true // other text in that event was sent already
		})
		if err == nil {
			out = append(out, wire.Event{Name: last.Name, Data: data})
		}
	}
	return out
}

func (r *Response) event(name string, data []byte) []byte {
	// With tokens, every string is looked at: a piece may end in the first
	// bytes of an escaped mark, which no test of the event's bytes finds.
	restore := r.tokens != nil
	if !restore && !r.scan {
		return nil
	}
	out, changed, err := rewrite(data, func(w *Walker, s string) (string, bool) {
		if ignored(w) {
			return "", false
		}
		path := ""
		if r.scan {
			path = w.Path()
			st := r.scans[path]
			if st == nil {
				if r.scans == nil {
					r.scans = map[string]*redact.Stream{}
				}
				st = r.e.r.Stream(redact.StreamOptions{})
				r.scans[path] = st
			}
			r.found(st.Write(s))
		}
		if !restore {
			return "", false
		}
		if path == "" {
			path = w.Path()
		}
		rs := r.restorers[path]
		if rs == nil {
			if !maybeToken(s) {
				return "", false
			}
			if r.restorers == nil {
				r.restorers = map[string]*redact.Restorer{}
			}
			rs = r.tokens.Restorer()
			r.restorers[path] = rs
		}
		out := rs.Write(s)
		if rs.Holding() {
			if r.last == nil {
				r.last = map[string]wire.Event{}
			}
			r.last[path] = wire.Event{Name: name, Data: bytes.Clone(data)}
		}
		if out != s {
			return out, true
		}
		return "", false
	})
	if err != nil || !changed {
		return nil
	}
	return out
}

// End finishes a streamed answer: the scan's last findings, and events
// carrying text still held back, to write before the stream ends. A second
// call returns nothing.
func (r *Response) End() []wire.Event {
	for _, path := range slices.Sorted(maps.Keys(r.scans)) {
		r.found(r.scans[path].Close())
	}
	r.scans = nil
	return r.release()
}

func (r *Response) found(fs []redact.Finding) {
	for _, f := range fs {
		h := r.hits[f.Detector]
		if h == nil {
			h = &Hit{Detector: f.Detector, Class: r.e.class[f.Detector], Action: Alert, Location: "response"}
			r.hits[f.Detector] = h
			r.order = append(r.order, f.Detector)
		}
		h.Count++
	}
}

// Hits are the findings in the answer.
func (r *Response) Hits() []Hit {
	out := make([]Hit, 0, len(r.order))
	for _, d := range r.order {
		out = append(out, *r.hits[d])
	}
	return out
}
