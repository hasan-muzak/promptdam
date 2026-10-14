package promptcache

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The scanner finds what encoding/json finds: a key's value (the last of
// repeated keys, escaped keys decoded) and an array's elements, through
// escaped quotes, backslash runs and nesting; malformed documents find
// nothing.
func TestScanAgreesWithDecoding(t *testing.T) {
	docs := []string{
		`{"messages":[{"role":"system","content":"a \"quoted\" \\ text"},{"role":"user","content":[{"type":"text","text":"]}"}]}],"x":1}`,
		`{ "a" : "\\\\", "messages" : [ 1 , true , null , -2.5e3 , "s" , { } , [ ] ] }`,
		`{"messages":[],"messages":[{"role":"tool"}]}`,
		`{"messages":[{"role":"developer"}]}`,
		`{"nested":{"messages":[1]},"messages":[{"content":"x"}]}`,
	}
	for _, doc := range docs {
		var want map[string]json.RawMessage
		if err := json.Unmarshal([]byte(doc), &want); err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		s, e, ok := member([]byte(doc), "messages")
		if !ok || !bytes.Equal(bytes.TrimSpace([]byte(doc[s:e])), bytes.TrimSpace(want["messages"])) {
			t.Fatalf("%s: messages %q, want %q", doc, doc[s:e], want["messages"])
		}
		var items []json.RawMessage
		if err := json.Unmarshal(want["messages"], &items); err != nil {
			t.Fatal(err)
		}
		spans := elements([]byte(doc), s, e)
		if len(spans) != len(items) {
			t.Fatalf("%s: %d elements, want %d", doc, len(spans), len(items))
		}
		for i, sp := range spans {
			if !bytes.Equal([]byte(doc[sp[0]:sp[1]]), bytes.TrimSpace(items[i])) {
				t.Fatalf("%s: element %d %q, want %q", doc, i, doc[sp[0]:sp[1]], items[i])
			}
		}
	}
	escaped := []byte(docs[3])
	s, e, _ := member(escaped, "messages")
	if role := roleOf(escaped, elements(escaped, s, e)[0]); role != "developer" {
		t.Fatalf("an escaped key's role: %q", role)
	}
	for _, bad := range []string{``, `[]`, `{"messages":`, `{"messages":[1,}`, `{"messages":"unterminated}`, `{"a" 1}`} {
		if _, _, ok := member([]byte(bad), "messages"); ok {
			t.Fatalf("malformed %q found messages", bad)
		}
	}
}

func FuzzMember(f *testing.F) {
	f.Add(`{"messages":[{"role":"system","content":"x"}]}`)
	f.Add(`{"a":"\"","messages":[1,[2,{"b":"]"}]]}`)
	f.Fuzz(func(t *testing.T, doc string) {
		var want map[string]json.RawMessage
		valid := json.Unmarshal([]byte(doc), &want) == nil
		s, e, ok := member([]byte(doc), "messages") // never panics
		if !valid || !ok {
			return
		}
		got, wanted := bytes.TrimSpace([]byte(doc[s:e])), bytes.TrimSpace(want["messages"])
		if !bytes.Equal(got, wanted) {
			t.Fatalf("%q: %q, want %q", doc, got, wanted)
		}
	})
}
