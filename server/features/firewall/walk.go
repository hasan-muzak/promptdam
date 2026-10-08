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

// Path is the value's place: its keys and indexes from the root, joined by
// dots (messages.3.content.0.text).
func (w *Walker) Path() string {
	var b strings.Builder
	for i, f := range w.frames {
		if i > 0 {
			b.WriteByte('.')
		}
		if f.object {
			b.WriteString(f.key)
		} else {
			b.WriteString(strconv.Itoa(f.index))
		}
	}
	return b.String()
}

var errMalformed = errors.New("firewall: the document isn't valid JSON")

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
		case '{':
			w.frames = append(w.frames, frame{object: true})
			expectKey = true
		case '[':
			w.frames = append(w.frames, frame{})
			expectKey = false
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
