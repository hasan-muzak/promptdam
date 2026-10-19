package firewall

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Walker is where a string value sits in a JSON document.
type Walker struct {
	frames []frame
}

type frame struct {
	object bool
	key    string // the object's current key
	index  int    // the array's current index
	// idx is an object's own "index" field when it came before the value
	// read (a streamed tool call's or content block's, which OpenAI and
	// Anthropic write first): it tells the object apart from others at the
	// same array position. An index written after the content isn't seen,
	// and the position alone places the value.
	idx    int
	hasIdx bool
}

// Top is the document's top-level key the value is under ("" for a value
// at the root).
func (w *Walker) Top() string {
	if len(w.frames) == 0 || !w.frames[0].object {
		return ""
	}
	return w.frames[0].key
}

// Key is the nearest object key the value is under: an array's elements
// are under its key.
func (w *Walker) Key() string {
	for i := len(w.frames) - 1; i >= 0; i-- {
		if w.frames[i].object {
			return w.frames[i].key
		}
	}
	return ""
}

// InPayload reports a value inside a tool's input (Anthropic's tool_use
// input, below the document's top level): there, keys like id or type are
// the tool's own fields, holding data, not the protocol's identifiers.
// The Responses API's top-level input is the protocol's own list.
func (w *Walker) InPayload() bool {
	for i := 1; i < len(w.frames)-1; i++ {
		if f := w.frames[i]; f.object && f.key == "input" {
			return true
		}
	}
	return false
}

// Path is the value's place: its keys and indexes from the root, joined by
// dots (messages.3.content.0.text). An object with its own index field
// carries it after its key (delta#1.partial_json), so parallel tool calls
// and content blocks, which can share an array position, keep apart.
func (w *Walker) Path() string {
	var b strings.Builder
	for i, f := range w.frames {
		if i > 0 {
			b.WriteByte('.')
		}
		if f.object {
			b.WriteString(f.key)
			if f.hasIdx {
				b.WriteByte('#')
				b.WriteString(strconv.Itoa(f.idx))
			}
		} else {
			b.WriteString(strconv.Itoa(f.index))
		}
	}
	return b.String()
}

var errMalformed = errors.New("firewall: the document isn't valid JSON")

// maxDepth is the deepest nesting read, as the request parser's
// (encoding/json): an answer nested deeper, from a provider that wants a
// gateway's memory, is left unread.
const maxDepth = 10_000

// rewrite calls fn with every string value (keys aside) of a JSON
// document; a replacement fn returns is spliced in its place, the rest of
// the document kept byte for byte. changed is false (and out doc itself)
// when nothing was replaced.
func rewrite(doc []byte, fn func(w *Walker, s string) (string, bool)) (out []byte, changed bool, err error) {
	w := &Walker{}
	copied := 0 // doc[:copied] is in out
	expectKey := false
	for i := 0; i < len(doc); i++ {
		switch c := doc[i]; c {
		case '{', '[':
			if len(w.frames) >= maxDepth {
				return nil, false, errMalformed
			}
			w.frames = append(w.frames, frame{object: c == '{'})
			expectKey = c == '{'
		case '}', ']':
			if len(w.frames) == 0 {
				return nil, false, errMalformed
			}
			w.frames = w.frames[:len(w.frames)-1]
		case ',':
			if n := len(w.frames); n > 0 {
				if w.frames[n-1].object {
					expectKey = true
				} else {
					w.frames[n-1].index++
				}
			}
		case '"':
			end, escaped, err := stringEnd(doc, i)
			if err != nil {
				return nil, false, err
			}
			raw := doc[i+1 : end]
			var s string
			if escaped {
				if err := json.Unmarshal(doc[i:end+1], &s); err != nil {
					return nil, false, errMalformed
				}
			} else {
				s = string(raw)
			}
			if expectKey {
				w.frames[len(w.frames)-1].key = s
				expectKey = false
			} else if repl, ok := fn(w, s); ok {
				out = append(out, doc[copied:i]...)
				out = appendString(out, repl)
				copied, changed = end+1, true
			}
			i = end
		default:
			if n := len(w.frames); n > 0 && c >= '0' && c <= '9' {
				if f := &w.frames[n-1]; f.object && f.key == "index" && !f.hasIdx {
					j := i
					for j < len(doc) && doc[j] >= '0' && doc[j] <= '9' && f.idx < 1<<20 {
						f.idx = f.idx*10 + int(doc[j]-'0')
						j++
					}
					f.hasIdx, i = true, j-1
				}
			}
		}
	}
	if !changed {
		return doc, false, nil
	}
	return append(out, doc[copied:]...), true, nil
}

// stringEnd finds the closing quote of the string starting at doc[i].
func stringEnd(doc []byte, i int) (end int, escaped bool, err error) {
	for j := i + 1; j < len(doc); j++ {
		switch doc[j] {
		case '\\':
			escaped = true
			j++
		case '"':
			return j, escaped, nil
		}
	}
	return 0, false, errMalformed
}

// appendString appends s as a JSON string, without escaping HTML
// characters, as providers write JSON.
func appendString(out []byte, s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return append(out, bytes.TrimRight(b.Bytes(), "\n")...)
}
