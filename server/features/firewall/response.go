package firewall

import (
	"bytes"
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

var tokenMark = []byte("⟦")

// Body filters a whole answer; nil when it is unchanged.
func (r *Response) Body(doc []byte) []byte {
	out, changed, err := rewrite(doc, func(w *Walker, s string) (string, bool) {
		if skipped[w.Key()] {
			return "", false
		}
		if r.scan {
			r.found(r.e.find(s))
		}
		if r.tokens != nil && bytes.Contains([]byte(s), tokenMark) {
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
	restore := r.tokens != nil && (bytes.Contains(data, tokenMark) || r.holding())
	if !restore && !r.scan {
		return nil
	}
	out, changed, err := rewrite(data, func(w *Walker, s string) (string, bool) {
		if skipped[w.Key()] {
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
			if !bytes.Contains([]byte(s), tokenMark) {
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

// holding reports a token held back from an earlier event.
func (r *Response) holding() bool { return len(r.restorers) > 0 }

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
