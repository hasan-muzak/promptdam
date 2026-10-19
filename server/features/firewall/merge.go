package firewall

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hasan-muzak/keel-core/pkg/redact"
)

// Span is one of the model's findings in a text: text[Start:End] (bytes),
// its label (a ModelLabels name), how sure the model is (0 to 1).
type Span struct {
	Start, End int
	Label      string
	Score      float64
}

// Reader is the detector model as Inspect asks it: the spans of each text,
// in order. The gateway's is the detector service's client
// (features/detector), which caches what it read before.
type Reader interface {
	Spans(ctx context.Context, texts []string) ([][]Span, error)
}

// SureSecret is how sure the model must be of a secret no pattern knows to
// block a call with it; below, the finding only alerts. A block stops an
// agent's session, so it waits for a sure one.
const SureSecret = 0.9

// finding is one place a value was found, by a pattern or the model, and
// what it gets.
type finding struct {
	Detector, Class, Action string
	Start, End              int
	Model                   bool
}

// modelReads reports a leaf the model reads: text, not the protocol's
// identifiers, a tool's definition, data, or thinking (signed, so its
// text must stay as the provider wrote it; it only gets the tokens found
// elsewhere).
func modelReads(l Leaf) bool {
	switch l.Top {
	case "tools", "functions", "tool_choice", "response_format", "metadata", "stop":
		return false
	}
	if l.Skip || l.Thinking || blob(l.S) || opaqueID(l.S) {
		return false
	}
	letters := 0
	for _, c := range l.S {
		if unicode.IsLetter(c) {
			if letters++; letters >= 2 {
				return true
			}
		}
	}
	return false
}

// opaqueID reports a string that is one identifier: a UUID or a hex hash.
func opaqueID(s string) bool {
	if len(s) < 16 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') && c != '-' {
			return false
		}
	}
	return true
}

// placeholders are what a secret's example value says instead of one.
var placeholders = []string{"xxxx", "your", "<", ">", "${", "***", "example", "changeme", "replace", "dummy", "redacted"}

// modelFindings are the findings the model's spans in s give, by the
// project's settings: labels off dropped, terms never masked dropped, code
// and placeholders where a secret would be dropped, and an unsure secret
// only alerting.
func (e *Engine) modelFindings(s string, spans []Span) []finding {
	var out []finding
	var allowed [][2]int
	if len(spans) > 0 {
		allowed = e.allowedRanges(s)
	}
	for _, sp := range spans {
		if sp.Start < 0 || sp.End > len(s) || sp.Start >= sp.End || !utf8.RuneStart(s[sp.Start]) ||
			sp.End < len(s) && !utf8.RuneStart(s[sp.End]) {
			continue // not a place in this text: never trusted
		}
		l, ok := modelLabel(sp.Label)
		if !ok {
			continue
		}
		action := e.s.labelAction(l)
		value := s[sp.Start:sp.End]
		if action == Off || inside(allowed, sp.Start, sp.End) {
			continue
		}
		if l.Class == ClassSecret {
			lower := strings.ToLower(value)
			if expression(value) || slices.ContainsFunc(placeholders, func(p string) bool { return strings.Contains(lower, p) }) {
				continue
			}
			if action == Block && sp.Score < SureSecret {
				action = Alert
			}
		}
		out = append(out, finding{Detector: l.Name, Class: l.Class, Action: action, Start: sp.Start, End: sp.End, Model: true})
	}
	return out
}

// allowedRanges are where s holds a term never masked, any case, as
// places in s: found in one read of s lowered, whose places are mapped back
// (lowering may change a character's length: İ).
func (e *Engine) allowedRanges(s string) [][2]int {
	if e.allow == nil {
		return nil
	}
	var b strings.Builder
	orig := make([]int, 0, len(s)+1) // orig[i]: the place in s of lowered byte i
	for i, r := range s {
		before := b.Len()
		b.WriteRune(unicode.ToLower(r))
		for range b.Len() - before {
			orig = append(orig, i)
		}
	}
	orig = append(orig, len(s))
	var out [][2]int
	for _, m := range e.allow.Find(b.String()) {
		out = append(out, [2]int{orig[m.Start], orig[m.End]})
	}
	return out
}

// inside reports [start, end) inside one of ranges.
func inside(ranges [][2]int, start, end int) bool {
	return slices.ContainsFunc(ranges, func(r [2]int) bool { return r[0] <= start && end <= r[1] })
}

// merge is a text's pattern findings and model findings as one list,
// leftmost first and apart: a pattern wins a place it shares with the
// model (it is exact, and a secret's pattern is the one to trust).
func merge(patterns, model []finding) []finding {
	out := slices.Clone(patterns)
	for _, m := range model {
		if !slices.ContainsFunc(out, func(f finding) bool { return f.Start < m.End && m.Start < f.End }) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b finding) int { return a.Start - b.Start })
	return dedupe(out)
}

// dedupe drops findings overlapping an earlier one: the request is spliced
// value by value, so they must be apart.
func dedupe(fs []finding) []finding {
	out := fs[:0]
	end := -1
	for _, f := range fs {
		if f.Start >= end {
			out = append(out, f)
			end = f.End
		}
	}
	return out
}

// patternFindings are the redactor's findings in s with what they get.
func (e *Engine) patternFindings(s string) []finding {
	if e.r == nil {
		return nil
	}
	fs := e.find(s)
	out := make([]finding, 0, len(fs))
	for _, f := range fs {
		class := e.class[f.Detector]
		out = append(out, finding{Detector: f.Detector, Class: class, Action: e.action(class), Start: f.Start, End: f.End})
	}
	return out
}

// consistent adds, to every leaf, the values the model found anywhere in
// the request where its findings don't already cover them: a first name
// said again, a name in a tool result, in thinking. Terms never masked stay
// so. Nothing is kept: the next call's request holds the history again.
func (e *Engine) consistent(ls []Leaf, found [][]finding) {
	var words []string
	var from []finding
	seen := map[string]bool{}
	for i, fs := range found {
		for _, f := range fs {
			v := ls[i].S[f.Start:f.End]
			if !f.Model || f.Action == Alert || utf8.RuneCountInString(v) < 3 || seen[v] {
				continue
			}
			seen[v] = true
			words = append(words, v)
			from = append(from, f)
		}
	}
	if len(words) == 0 {
		return
	}
	lit := redact.NewLiterals(words)
	for i, l := range ls {
		if l.Skip || blob(l.S) {
			continue
		}
		var added []finding
		var allowed [][2]int
		ms := lit.Find(l.S)
		if len(ms) > 0 {
			allowed = e.allowedRanges(l.S)
		}
		for _, m := range ms {
			if !alone(l.S, m.Start, m.End) || inside(allowed, m.Start, m.End) {
				continue
			}
			if slices.ContainsFunc(found[i], func(f finding) bool { return f.Start < m.End && m.Start < f.End }) {
				continue
			}
			f := from[m.Word]
			f.Start, f.End = m.Start, m.End
			added = append(added, f)
		}
		if added != nil {
			found[i] = append(found[i], added...)
			slices.SortFunc(found[i], func(a, b finding) int { return a.Start - b.Start })
			found[i] = dedupe(found[i])
		}
	}
}

// alone reports s[start:end] standing as a word of its own: not inside a
// longer word, in scripts that put spaces between words (in Chinese,
// Japanese or Thai a name is found where it is).
func alone(s string, start, end int) bool {
	if start > 0 {
		before, _ := utf8.DecodeLastRuneInString(s[:start])
		first, _ := utf8.DecodeRuneInString(s[start:])
		if wordRune(before) && wordRune(first) {
			return false
		}
	}
	if end < len(s) {
		after, _ := utf8.DecodeRuneInString(s[end:])
		last, _ := utf8.DecodeLastRuneInString(s[:end])
		if wordRune(after) && wordRune(last) {
			return false
		}
	}
	return true
}

// wordRune is a letter or digit of a script with spaces between words.
func wordRune(r rune) bool {
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
		return false
	}
	return !unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao, unicode.Khmer,
		unicode.Myanmar)
}
