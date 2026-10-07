// Package classifier learns, for each model router, which calls each of its
// tiers holds up on, from the router's judged samples (router_evals), and
// predicts it for new calls in the gateway (project.md §6.2): features,
// training, calibration and inference.
//
// Features are hashes: words and word pairs of the call's last user
// message and of its instructions, its family, tags and shape. They tell
// "count / list / extract" from "why / prove / is it legal" without the
// text ever being stored.
package classifier

import (
	"bytes"
	"encoding/json"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// Dim is the number of feature buckets features are hashed into.
const Dim = 1 << 18

// Bounds on what is read of a call's text.
const (
	maxUserTokens   = 400
	maxSystemTokens = 200
	maxTokenLen     = 32
)

// Input is what a call's features are computed from.
type Input struct {
	// System is the instructions; LastUser the text of the last user
	// message (tool results included).
	System, LastUser string
	Family           string
	Tags             []string
	// InputTokens is estimated (the body's bytes / 4).
	InputTokens, MaxTokens, Messages int
	Tools, JSON, Stream, Images      bool
}

// InputOf reads a call's features' input from its request.
func InputOf(f wire.Format, req wire.Request, body []byte, family string, tags []string) Input {
	images := bytes.Contains(body, []byte(`"image_url"`)) || HasType(body, "image")
	return Input{System: req.Prefix.System, LastUser: LastUser(f, body), Family: family, Tags: tags,
		InputTokens: len(body) / 4, MaxTokens: req.MaxTokens, Messages: req.Messages, Tools: req.Tools,
		JSON: req.Prefix.Format != "", Stream: req.Stream, Images: images}
}

// Features are the call's feature buckets, sorted and unique.
func Features(in Input) []uint32 {
	out := make([]uint32, 0, 2*maxUserTokens+maxSystemTokens+16)
	add := func(prefix, value string) { out = append(out, hash(prefix, value)) }
	words(in.LastUser, maxUserTokens, func(prev, w []byte) {
		out = append(out, hash("u:", w))
		if len(prev) > 0 {
			out = append(out, hash2("b:", prev, w))
		}
	})
	words(in.System, maxSystemTokens, func(_, w []byte) { out = append(out, hash("s:", w)) })
	if in.Family != "" {
		add("fam:", in.Family)
	}
	for _, t := range in.Tags {
		add("tag:", t)
	}
	add("msgs:", strconv.Itoa(bucket(in.Messages)))
	add("in:", strconv.Itoa(bucket(in.InputTokens)))
	add("max:", strconv.Itoa(bucket(in.MaxTokens)))
	add("len:", strconv.Itoa(bucket(len(in.LastUser))))
	for _, f := range []struct {
		on   bool
		name string
	}{{in.Tools, "tools"}, {in.JSON, "json"}, {in.Stream, "stream"}, {in.Images, "images"},
		{strings.Contains(in.LastUser, "```"), "code"}} {
		if f.on {
			add("has:", f.name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// bucket is n's power of two: 0 for 0, else 1 + floor(log2 n).
func bucket(n int) int {
	if n <= 0 {
		return 0
	}
	return bits.Len(uint(n))
}

// words calls fn with each lowercase word of s (letters and digits) and
// the word before it, up to max words. The slices are valid during the
// call only.
func words(s string, max int, fn func(prev, w []byte)) {
	var bufs [2][maxTokenLen * utf8.UTFMax]byte
	var prev []byte
	for i, n := 0, 0; i < len(s) && n < max; {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			i += size
			continue
		}
		w := bufs[n%2][:0]
		runes := 0
		for i < len(s) {
			r, size = utf8.DecodeRuneInString(s[i:])
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				break
			}
			if runes < maxTokenLen {
				w = utf8.AppendRune(w, unicode.ToLower(r))
				runes++
			}
			i += size
		}
		fn(prev, w)
		prev = w
		n++
	}
}

// FNV-1a, 32 bits.
const (
	offset32 = 2166136261
	prime32  = 16777619
)

func hash[T string | []byte](prefix string, value T) uint32 {
	h := uint32(offset32)
	for i := 0; i < len(prefix); i++ {
		h = (h ^ uint32(prefix[i])) * prime32
	}
	for i := 0; i < len(value); i++ {
		h = (h ^ uint32(value[i])) * prime32
	}
	return h & (Dim - 1)
}

func hash2(prefix string, a, b []byte) uint32 {
	h := uint32(offset32)
	for i := 0; i < len(prefix); i++ {
		h = (h ^ uint32(prefix[i])) * prime32
	}
	for i := 0; i < len(a); i++ {
		h = (h ^ uint32(a[i])) * prime32
	}
	h = (h ^ ' ') * prime32
	for i := 0; i < len(b); i++ {
		h = (h ^ uint32(b[i])) * prime32
	}
	return h & (Dim - 1)
}

// LastUser is the text of a request's last user message: its text parts
// and tool results' text.
func LastUser(f wire.Format, body []byte) string {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role == "user" || m.Role == "tool" && f == wire.OpenAIChat {
			return contentText(m.Content)
		}
	}
	return contentText(req.Input)
}

// contentText is the text of a content: a string, or the text of its
// parts and of the tool results among them.
func contentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b bytes.Buffer
	for _, p := range parts {
		s := p.Text
		if p.Type == "tool_result" {
			s = contentText(p.Content)
		}
		if s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
	}
	return b.String()
}

// HasType reports a JSON member "type" whose value is the string name,
// whatever the spacing: a content part of that type. It reads the body
// once.
func HasType(body []byte, name string) bool {
	key := []byte(`"type"`)
	for i := 0; ; {
		j := bytes.Index(body[i:], key)
		if j < 0 {
			return false
		}
		k := i + j + len(key)
		for k < len(body) && isSpace(body[k]) {
			k++
		}
		if k < len(body) && body[k] == ':' {
			k++
			for k < len(body) && isSpace(body[k]) {
				k++
			}
			if k+len(name)+2 <= len(body) && body[k] == '"' && string(body[k+1:k+1+len(name)]) == name && body[k+1+len(name)] == '"' {
				return true
			}
		}
		i = i + j + len(key)
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
