package promptcache

import (
	"bytes"
	"encoding/json"
)

// A JSON scanner that finds where values sit without decoding them: one
// pass over the bytes, strings skipped with a byte search for their
// closing quote. It reads only what the gateway already parsed whole, so
// a malformed document just reports nothing found.

// member is the span [start, end) of the value of a top-level key of a JSON
// object: the last occurrence, the one decoders keep.
func member(obj []byte, key string) (start, end int, ok bool) {
	i := skipSpace(obj, 0)
	if i >= len(obj) || obj[i] != '{' {
		return 0, 0, false
	}
	i++
	for {
		if i = skipSpace(obj, i); i >= len(obj) {
			return 0, 0, false
		}
		if obj[i] == '}' {
			return start, end, ok
		}
		if obj[i] != '"' {
			return 0, 0, false
		}
		ke, found := stringEnd(obj, i)
		if !found {
			return 0, 0, false
		}
		name := obj[i:ke]
		if i = skipSpace(obj, ke); i >= len(obj) || obj[i] != ':' {
			return 0, 0, false
		}
		i = skipSpace(obj, i+1)
		ve, found := skipValue(obj, i)
		if !found {
			return 0, 0, false
		}
		if keyIs(name, key) {
			start, end, ok = i, ve, true
		}
		switch i = skipSpace(obj, ve); {
		case i < len(obj) && obj[i] == ',':
			i++
		case i < len(obj) && obj[i] == '}':
			return start, end, ok
		default:
			return 0, 0, false
		}
	}
}

// elements are the spans of the elements of the JSON array at doc[start:end].
func elements(doc []byte, start, end int) [][2]int {
	arr := doc[:end]
	i := skipSpace(arr, start)
	if i >= len(arr) || arr[i] != '[' {
		return nil
	}
	var spans [][2]int
	for i++; ; {
		if i = skipSpace(arr, i); i >= len(arr) {
			return nil
		}
		if arr[i] == ']' {
			return spans
		}
		e, ok := skipValue(arr, i)
		if !ok {
			return nil
		}
		spans = append(spans, [2]int{i, e})
		switch i = skipSpace(arr, e); {
		case i < len(arr) && arr[i] == ',':
			i++
		case i < len(arr) && arr[i] == ']':
			return spans
		default:
			return nil
		}
	}
}

// skipValue is the end of the JSON value starting at doc[i].
func skipValue(doc []byte, i int) (int, bool) {
	if i >= len(doc) {
		return 0, false
	}
	switch doc[i] {
	case '"':
		return stringEnd(doc, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(doc); j++ {
			switch doc[j] {
			case '"':
				e, ok := stringEnd(doc, j)
				if !ok {
					return 0, false
				}
				j = e - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1, true
				}
			}
		}
		return 0, false
	default: // a number, true, false or null
		j := i
		for ; j < len(doc); j++ {
			switch doc[j] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return j, j > i
			}
		}
		return j, j > i
	}
}

// stringEnd is the position after the closing quote of the string starting
// at doc[i]. A quote is escaped when an odd run of backslashes precedes it;
// each run precedes one quote, so the counting stays linear.
func stringEnd(doc []byte, i int) (int, bool) {
	for j := i + 1; ; {
		k := bytes.IndexByte(doc[j:], '"')
		if k < 0 {
			return 0, false
		}
		q := j + k
		n := 0
		for p := q - 1; p > i && doc[p] == '\\'; p-- {
			n++
		}
		if n%2 == 0 {
			return q + 1, true
		}
		j = q + 1
	}
}

func skipSpace(doc []byte, i int) int {
	for i < len(doc) && (doc[i] == ' ' || doc[i] == '\t' || doc[i] == '\n' || doc[i] == '\r') {
		i++
	}
	return i
}

// keyIs reports whether a raw JSON key (quotes included) is key: compared as
// written, decoded only when it has escapes.
func keyIs(raw []byte, key string) bool {
	if len(raw) == len(key)+2 && string(raw[1:len(raw)-1]) == key {
		return true
	}
	if bytes.IndexByte(raw, '\\') < 0 {
		return false
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == key
}

// stringValue is a raw JSON string's value ("" when raw isn't a string).
func stringValue(raw []byte) string {
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' && bytes.IndexByte(raw, '\\') < 0 {
		return string(raw[1 : len(raw)-1])
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
