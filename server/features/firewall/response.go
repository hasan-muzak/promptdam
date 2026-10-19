package firewall

import (
	"maps"
	"slices"

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

// Event filters one event of a streamed answer (its data); nil when it is
// unchanged.
func (r *Response) Event(data []byte) []byte {
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
		if out := rs.Write(s); out != s {
			return out, true
		}
		return "", false
	})
	if err != nil || !changed {
		return nil
	}
	return out
}

// End finishes the scan of a streamed answer.
func (r *Response) End() {
	for _, path := range slices.Sorted(maps.Keys(r.scans)) {
		r.found(r.scans[path].Close())
	}
	r.scans = nil
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
