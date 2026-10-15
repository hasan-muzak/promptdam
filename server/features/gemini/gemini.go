// Package gemini serves Google's Gemini API (generateContent) to clients,
// as a door to the gateway: Google's SDKs (google-genai for Python, JS, Go
// and Java, Vercel's @ai-sdk/google, LangChain) call Promptdam with their
// base URL changed. A request is turned into an OpenAI Chat Completions
// one at the edge, so routers, fallbacks, caching, the firewall and the
// ledger work as for any call, and the answer (whole, streamed or an
// error) is turned back.
//
// Chat is the door's pivot because Gemini models are already called
// through Google's OpenAI-compatible API, and Chat carries Gemini 3's
// thought signatures (tool_calls[].extra_content.google.thought_signature),
// which Messages has no place for. Claude goes on through the translator.
//
// Content is never dropped: what Chat can't express (Google's own server
// tools, its context cache, files uploaded to Google, video) is refused
// with a *translate.Error naming it. Tuning parameters without an
// equivalent are left out and listed, for X-Promptdam-Dropped.
package gemini

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// Op is what a path of Gemini's API asks for.
type Op uint8

// Ops.
const (
	OpGenerate Op = iota + 1
	OpStream
	OpCount
	OpEmbed
	OpBatchEmbed
	OpList // GET models
	OpGet  // GET models/{model}
)

var methods = map[string]Op{
	"generateContent": OpGenerate, "streamGenerateContent": OpStream, "countTokens": OpCount,
	"embedContent": OpEmbed, "batchEmbedContents": OpBatchEmbed,
}

// versions are the API versions the SDKs call: v1beta by default, v1 and
// v1alpha by choice, v1beta1 on Vertex.
var versions = []string{"v1beta", "v1", "v1alpha", "v1beta1"}

// Call is a path's operation and model.
type Call struct {
	Op    Op
	Model string // without "models/" or Vertex's publisher path
}

// ParsePath reads a path of Gemini's API, the part after the door's
// prefix: {version}/models/{model}:{method}, Vertex's
// {version}/publishers/google/models/… and
// {version}/projects/{p}/locations/{l}/publishers/google/models/… (the
// project and location are the provider's, set up in Promptdam), and
// {version}/models[/{model}] for the model list; another publisher's
// model on Vertex is {publisher}/{model}. The method follows the
// last colon, so a router's tier (advisor:fast) is part of the model.
func ParsePath(p string) (Call, bool) {
	version, rest, ok := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	if !ok || !slices.Contains(versions, version) {
		return Call{}, false
	}
	if r, ok := strings.CutPrefix(rest, "projects/"); ok {
		parts := strings.SplitN(r, "/", 4) // {p}, locations, {l}, rest
		if len(parts) != 4 || parts[1] != "locations" {
			return Call{}, false
		}
		rest = parts[3]
	}
	// Vertex names other publishers' models publishers/{p}/models/{m}:
	// {p}/{m} to Promptdam, as its Vertex providers serve them.
	publisher := ""
	if r, ok := strings.CutPrefix(rest, "publishers/"); ok {
		pub, r, ok := strings.Cut(r, "/")
		if !ok || pub == "" {
			return Call{}, false
		}
		if pub != "google" {
			publisher = pub + "/"
		}
		rest = r
	}
	if rest == "models" {
		return Call{Op: OpList}, true
	}
	m, ok := strings.CutPrefix(rest, "models/")
	if !ok || m == "" {
		return Call{}, false
	}
	i := strings.LastIndexByte(m, ':')
	if i < 0 {
		return Call{Op: OpGet, Model: publisher + m}, true
	}
	op, ok := methods[m[i+1:]]
	if !ok || i == 0 {
		return Call{}, false
	}
	return Call{Op: op, Model: publisher + m[:i]}, true
}

// obj is a JSON object of Gemini's API. Protobuf's JSON takes a field in
// lowerCamelCase (as the SDKs send it) or in snake_case (as Google's REST
// examples write it), so both are read. Free-form values (a function's
// arguments, a schema's property names) stay raw.
type obj map[string]json.RawMessage

func readObj(raw json.RawMessage) (obj, bool) {
	if null(raw) {
		return nil, true
	}
	var o obj
	if json.Unmarshal(raw, &o) != nil || o == nil {
		return nil, false
	}
	return o, true
}

// get is the field name (lowerCamelCase), in either spelling.
func (o obj) get(name string) json.RawMessage {
	if v, ok := o[name]; ok {
		return v
	}
	return o[snake(name)]
}

func (o obj) has(name string) bool { return !null(o.get(name)) }

func (o obj) str(name string) string {
	var s string
	_ = json.Unmarshal(o.get(name), &s)
	return s
}

// list is a field holding a list of objects.
func (o obj) list(name string) ([]obj, bool) {
	raw := o.get(name)
	if null(raw) {
		return nil, true
	}
	var out []obj
	if json.Unmarshal(raw, &out) != nil {
		return nil, false
	}
	return out, true
}

// keys are the object's fields, in lowerCamelCase.
func (o obj) keys() []string {
	out := make([]string, 0, len(o))
	for k := range o {
		out = append(out, camel(k))
	}
	slices.Sort(out)
	return out
}

// snake is a lowerCamelCase name in snake_case.
func snake(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if 'A' <= c && c <= 'Z' {
			b.WriteByte('_')
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// camel is a snake_case name in lowerCamelCase (one already camel is kept).
func camel(name string) string {
	if !strings.Contains(name, "_") {
		return name
	}
	var b strings.Builder
	up := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_':
			up = true
			continue
		case up && 'a' <= c && c <= 'z':
			c -= 'a' - 'A'
		}
		up = false
		b.WriteByte(c)
	}
	return b.String()
}

// null reports an absent or null JSON value.
func null(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

func refuse(msg string) error { return &translate.Error{Message: msg} }

// marshal encodes v without escaping HTML characters, as providers write
// JSON.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
