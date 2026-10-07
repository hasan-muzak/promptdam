package resolver

import (
	"bytes"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// Needs is what a call asks of a model: the features its request uses and
// the context it fills. Router tiers whose model lacks one are skipped.
type Needs struct {
	Tools, Images, Files, JSONSchema bool
	// Tokens estimates the context the call fills: its input (the body's
	// bytes / 4) and its answer's limit. Unknown (0) with images, whose
	// bytes say nothing of their tokens.
	Tokens int
}

func needsOf(c *gateway.Call) Needs {
	n := Needs{
		Tools:      c.Request.Tools,
		JSONSchema: strings.HasPrefix(c.Request.Prefix.Format, "json_schema"),
		Images:     bytes.Contains(c.Body, []byte(`"image_url"`)) || hasType(c.Body, "image"),
		Files:      hasType(c.Body, "file") || hasType(c.Body, "document"),
	}
	if !n.Images {
		n.Tokens = c.BodyBytes/4 + c.Request.MaxTokens
	}
	return n
}

// lack is what model m lacks to take the call: a feature (tools, images,
// files, json_schema), "context", or "" when it can. A feature the price
// list says nothing about counts as present: a provider that refuses the
// call passes it to the fallbacks.
func (n Needs) lack(m llm.Model) string {
	for _, f := range []struct {
		need bool
		name string
	}{{n.Tools, llm.FeatureTools}, {n.Images, llm.FeatureImages}, {n.Files, llm.FeatureFiles}, {n.JSONSchema, llm.FeatureJSONSchema}} {
		if has, known := m.Has(f.name); f.need && known && !has {
			return f.name
		}
	}
	if n.Tokens > 0 && m.ContextWindow > 0 && n.Tokens > int(m.ContextWindow) {
		return "context"
	}
	return ""
}

// hasType reports a JSON member "type" whose value is the string name,
// whatever the spacing: a content part of that type. It reads the body
// once.
func hasType(body []byte, name string) bool {
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
