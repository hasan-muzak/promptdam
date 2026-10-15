// Package converse speaks AWS Bedrock's Converse API for the gateway, as a
// dialect of Anthropic's Messages: a request is translated as it is sent,
// and the answer is turned back into Messages as it arrives (a whole one,
// the binary event stream as Anthropic's server-sent events, an AWS error
// as Anthropic's), so everything after it reads Messages. Converse is the
// one API every Bedrock model takes: Amazon Nova, Meta Llama, Mistral,
// Cohere, DeepSeek, Qwen, MiniMax, gpt-oss and the Claude models Bedrock's
// Anthropic API doesn't serve.
package converse

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// Caching is how a model caches what a request marks: Claude anywhere,
// lifetimes kept; Nova in the system prompt and messages only, five
// minutes; the others not at all (Converse refuses their checkpoints).
type Caching int

// Cache families.
const (
	CachesNothing Caching = iota
	CachesClaude
	CachesNova
)

// CachingOf is a Bedrock model's cache family, by its id.
func CachingOf(model string) Caching {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "anthropic.claude") || strings.HasPrefix(m, "claude-"):
		return CachesClaude
	case strings.Contains(m, "amazon.nova"):
		return CachesNova
	}
	return CachesNothing
}

// claude reports a Claude model, which takes Anthropic's own fields
// (thinking, top_k, effort) through additionalModelRequestFields.
func claude(model string) bool { return CachingOf(model) == CachesClaude }

// The Messages request, as far as Converse can carry it.
type msgRequest struct {
	MaxTokens     int              `json:"max_tokens"`
	System        json.RawMessage  `json:"system"`
	Messages      []msgMessage     `json:"messages"`
	Tools         []msgTool        `json:"tools"`
	ToolChoice    *msgToolChoice   `json:"tool_choice"`
	Temperature   *float64         `json:"temperature"`
	TopP          *float64         `json:"top_p"`
	TopK          json.RawMessage  `json:"top_k"`
	StopSequences []string         `json:"stop_sequences"`
	Metadata      *msgMetadata     `json:"metadata"`
	Thinking      json.RawMessage  `json:"thinking"`
	OutputConfig  *msgOutputConfig `json:"output_config"`
	CacheControl  *msgCacheControl `json:"cache_control"`
}

type msgMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type msgBlock struct {
	Type         string           `json:"type"`
	Text         string           `json:"text"`
	Source       *msgSource       `json:"source"`
	Title        string           `json:"title"`
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Input        json.RawMessage  `json:"input"`
	ToolUseID    string           `json:"tool_use_id"`
	Content      json.RawMessage  `json:"content"`
	IsError      bool             `json:"is_error"`
	Thinking     string           `json:"thinking"`
	Signature    string           `json:"signature"`
	Data         string           `json:"data"`
	CacheControl *msgCacheControl `json:"cache_control"`
}

type msgSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	URL       string `json:"url"`
}

type msgTool struct {
	Type         string           `json:"type"`
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	InputSchema  json.RawMessage  `json:"input_schema"`
	CacheControl *msgCacheControl `json:"cache_control"`
}

type msgToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type msgMetadata struct {
	UserID string `json:"user_id"`
}

type msgOutputConfig struct {
	Effort string          `json:"effort"`
	Format json.RawMessage `json:"format"`
}

type msgCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl"`
}

// The Converse request.
type cRequest struct {
	Messages                     []cMessage        `json:"messages"`
	System                       []cBlock          `json:"system,omitempty"`
	InferenceConfig              *cInference       `json:"inferenceConfig,omitempty"`
	ToolConfig                   *cToolConfig      `json:"toolConfig,omitempty"`
	AdditionalModelRequestFields map[string]any    `json:"additionalModelRequestFields,omitempty"`
	RequestMetadata              map[string]string `json:"requestMetadata,omitempty"`
}

type cMessage struct {
	Role    string   `json:"role"`
	Content []cBlock `json:"content"`
}

// cBlock is one of Converse's content blocks: exactly one field set.
type cBlock struct {
	Text             *string      `json:"text,omitempty"`
	Image            *cImage      `json:"image,omitempty"`
	Document         *cDocument   `json:"document,omitempty"`
	ToolUse          *cToolUse    `json:"toolUse,omitempty"`
	ToolResult       *cToolResult `json:"toolResult,omitempty"`
	ReasoningContent *cReasoning  `json:"reasoningContent,omitempty"`
	CachePoint       *cCachePoint `json:"cachePoint,omitempty"`
	JSON             any          `json:"json,omitempty"`
}

type cImage struct {
	Format string  `json:"format"`
	Source cSource `json:"source"`
}

type cDocument struct {
	Format string  `json:"format"`
	Name   string  `json:"name"`
	Source cSource `json:"source"`
}

// cSource holds a blob, base64 as the JSON protocol sends it.
type cSource struct {
	Bytes string `json:"bytes"`
}

type cToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type cToolResult struct {
	ToolUseID string   `json:"toolUseId"`
	Content   []cBlock `json:"content"`
	Status    string   `json:"status,omitempty"`
}

type cReasoning struct {
	ReasoningText   *cReasoningText `json:"reasoningText,omitempty"`
	RedactedContent string          `json:"redactedContent,omitempty"`
}

type cReasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

type cCachePoint struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type cInference struct {
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

type cToolConfig struct {
	Tools      []cTool `json:"tools"`
	ToolChoice any     `json:"toolChoice,omitempty"`
}

type cTool struct {
	ToolSpec   *cToolSpec   `json:"toolSpec,omitempty"`
	CachePoint *cCachePoint `json:"cachePoint,omitempty"`
}

type cToolSpec struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	InputSchema cToolJSON `json:"inputSchema"`
}

type cToolJSON struct {
	JSON json.RawMessage `json:"json"`
}

// known are the Messages fields Request reads; others are dropped, listed.
var known = []string{"model", "stream", "max_tokens", "system", "messages", "tools", "tool_choice", "temperature", "top_p",
	"top_k", "stop_sequences", "metadata", "thinking", "output_config", "cache_control"}

// Request translates a Messages request into Converse's for model, a
// Bedrock id (the URL names it: Converse's body doesn't). dropped lists the
// fields Converse can't carry for the model, sorted; a request it can't
// express at all (an image by URL, a server tool) is refused
// (*translate.Error), so the gateway tries the next target.
func Request(body []byte, model string) (out []byte, dropped []string, err error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, nil, &translate.Error{Message: "the request isn't a JSON object"}
	}
	var in msgRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, nil, &translate.Error{Message: "the request doesn't read as Messages: " + err.Error()}
	}
	for k := range top {
		if !slices.Contains(known, k) {
			dropped = append(dropped, k)
		}
	}
	t := translator{model: model, caching: CachingOf(model), dropped: map[string]bool{}}
	req := cRequest{InferenceConfig: &cInference{MaxTokens: in.MaxTokens, Temperature: in.Temperature, TopP: in.TopP,
		StopSequences: in.StopSequences}}
	if req.System, err = t.system(in.System); err != nil {
		return nil, nil, err
	}
	if req.Messages, err = t.messages(in.Messages); err != nil {
		return nil, nil, err
	}
	if in.CacheControl != nil && len(req.Messages) > 0 {
		// Anthropic's automatic breakpoint: after the conversation so far.
		last := &req.Messages[len(req.Messages)-1]
		last.Content = t.cachePoint(last.Content, in.CacheControl, "messages")
	}
	if req.ToolConfig, err = t.tools(in.Tools, in.ToolChoice, req.Messages); err != nil {
		return nil, nil, err
	}
	extra := map[string]any{}
	if claude(model) {
		if len(in.TopK) > 0 && string(in.TopK) != "null" {
			extra["top_k"] = in.TopK
		}
		if len(in.Thinking) > 0 && string(in.Thinking) != "null" {
			extra["thinking"] = in.Thinking
		}
		if in.OutputConfig != nil && in.OutputConfig.Effort != "" {
			extra["output_config"] = map[string]string{"effort": in.OutputConfig.Effort}
		}
	} else {
		for _, k := range []string{"top_k", "thinking"} {
			if v, ok := top[k]; ok && string(v) != "null" {
				t.dropped[k] = true
			}
		}
		if in.OutputConfig != nil && in.OutputConfig.Effort != "" {
			t.dropped["output_config.effort"] = true
		}
	}
	if in.OutputConfig != nil && len(in.OutputConfig.Format) > 0 && string(in.OutputConfig.Format) != "null" {
		t.dropped["output_config.format"] = true
	}
	if len(extra) > 0 {
		req.AdditionalModelRequestFields = extra
	}
	if in.Metadata != nil && in.Metadata.UserID != "" {
		if metadataValue.MatchString(in.Metadata.UserID) {
			req.RequestMetadata = map[string]string{"user_id": in.Metadata.UserID}
		} else {
			t.dropped["metadata.user_id"] = true
		}
	}
	for k := range t.dropped {
		dropped = append(dropped, k)
	}
	slices.Sort(dropped)
	out, err = json.Marshal(req)
	return out, dropped, err
}

// metadataValue is what Converse's requestMetadata takes.
var metadataValue = regexp.MustCompile(`^[a-zA-Z0-9\s:_@$#=/+,.-]{1,256}$`)

type translator struct {
	model   string
	caching Caching
	dropped map[string]bool
	// cachePoints counts the checkpoints placed: Converse takes four.
	cachePoints int
}

// cachePoint appends a checkpoint after blocks, for a block the request
// marked, where the model caches (where is "system", "messages", "tools").
func (t *translator) cachePoint(blocks []cBlock, cc *msgCacheControl, where string) []cBlock {
	cp := t.point(cc, where)
	if cp == nil {
		return blocks
	}
	return append(blocks, cBlock{CachePoint: cp})
}

// point is a checkpoint for a marked block, nil where the model takes none.
func (t *translator) point(cc *msgCacheControl, where string) *cCachePoint {
	if cc == nil {
		return nil
	}
	switch {
	case t.caching == CachesNothing, t.caching == CachesNova && where == "tools", t.cachePoints == 4:
		t.dropped["cache_control"] = true
		return nil
	}
	t.cachePoints++
	cp := &cCachePoint{Type: "default"}
	if t.caching == CachesClaude && cc.TTL == "1h" {
		cp.TTL = "1h"
	}
	return cp
}

// system is the system prompt as text blocks.
func (t *translator) system(raw json.RawMessage) ([]cBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return []cBlock{{Text: &s}}, nil
	}
	var blocks []msgBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, &translate.Error{Message: "the system prompt doesn't read"}
	}
	var out []cBlock
	for _, b := range blocks {
		if b.Type != "text" {
			return nil, &translate.Error{Message: "a " + b.Type + " block in the system prompt"}
		}
		if b.Text != "" {
			text := b.Text
			out = append(out, cBlock{Text: &text})
		}
		out = t.cachePoint(out, b.CacheControl, "system")
	}
	return out, nil
}

// messages are the conversation, its consecutive messages of one role
// joined (Converse takes them alternating).
func (t *translator) messages(in []msgMessage) ([]cMessage, error) {
	var out []cMessage
	for _, m := range in {
		if m.Role != "user" && m.Role != "assistant" {
			return nil, &translate.Error{Message: fmt.Sprintf("a message of role %q", m.Role)}
		}
		blocks, err := t.content(m.Content, m.Role)
		if err != nil {
			return nil, err
		}
		if len(blocks) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			continue
		}
		out = append(out, cMessage{Role: m.Role, Content: blocks})
	}
	return out, nil
}

// content is a message's content as Converse blocks.
func (t *translator) content(raw json.RawMessage, role string) ([]cBlock, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return []cBlock{{Text: &s}}, nil
	}
	var blocks []msgBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, &translate.Error{Message: "a message's content doesn't read"}
	}
	var out []cBlock
	for i, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			text := b.Text
			out = append(out, cBlock{Text: &text})
		case "image":
			img, err := image(b.Source)
			if err != nil {
				return nil, err
			}
			out = append(out, cBlock{Image: img})
		case "document":
			doc, err := document(b, i)
			if err != nil {
				return nil, err
			}
			out = append(out, cBlock{Document: doc})
		case "tool_use":
			input := b.Input
			if len(input) == 0 || string(input) == "null" {
				input = json.RawMessage(`{}`)
			}
			out = append(out, cBlock{ToolUse: &cToolUse{ToolUseID: b.ID, Name: b.Name, Input: input}})
		case "tool_result":
			res, err := t.toolResult(b)
			if err != nil {
				return nil, err
			}
			out = append(out, cBlock{ToolResult: res})
		case "thinking", "redacted_thinking":
			if role != "assistant" || !claude(t.model) {
				t.dropped["thinking blocks"] = true
				continue
			}
			r := &cReasoning{RedactedContent: b.Data}
			if b.Type == "thinking" {
				r = &cReasoning{ReasoningText: &cReasoningText{Text: b.Thinking, Signature: b.Signature}}
			}
			out = append(out, cBlock{ReasoningContent: r})
		default:
			return nil, &translate.Error{Message: "a " + b.Type + " block (Converse takes text, images, documents, tools and thinking)"}
		}
		out = t.cachePoint(out, b.CacheControl, "messages")
	}
	return out, nil
}

// image is an image block; Converse takes its bytes, never a URL.
func image(src *msgSource) (*cImage, error) {
	if src == nil || src.Type != "base64" {
		return nil, &translate.Error{Message: "an image by URL or file (Converse takes the image itself)"}
	}
	format, ok := strings.CutPrefix(src.MediaType, "image/")
	if !ok || !slices.Contains([]string{"png", "jpeg", "gif", "webp"}, format) {
		return nil, &translate.Error{Message: "an image of type " + src.MediaType}
	}
	return &cImage{Format: format, Source: cSource{Bytes: src.Data}}, nil
}

// documentName is what Converse allows in a document's name.
var documentName = regexp.MustCompile(`[^a-zA-Z0-9\s()\[\]-]+`)

// document is a PDF or plain-text document block, named (Converse needs a
// name: the title, else its place).
func document(b msgBlock, i int) (*cDocument, error) {
	src := b.Source
	if src == nil {
		return nil, &translate.Error{Message: "a document without a source"}
	}
	name := strings.Join(strings.Fields(documentName.ReplaceAllString(b.Title, " ")), " ")
	if name == "" {
		name = fmt.Sprintf("document %d", i+1)
	}
	if len(name) > 200 {
		name = name[:200]
	}
	switch {
	case src.Type == "base64" && src.MediaType == "application/pdf":
		return &cDocument{Format: "pdf", Name: name, Source: cSource{Bytes: src.Data}}, nil
	case src.Type == "text":
		return &cDocument{Format: "txt", Name: name, Source: cSource{Bytes: base64Of(src.Data)}}, nil
	}
	return nil, &translate.Error{Message: "a document by URL or file (Converse takes the document itself)"}
}

// toolResult is a tool's result: its text, JSON or images.
func (t *translator) toolResult(b msgBlock) (*cToolResult, error) {
	res := &cToolResult{ToolUseID: b.ToolUseID}
	if b.IsError {
		res.Status = "error"
	}
	var s string
	switch {
	case len(b.Content) == 0 || string(b.Content) == "null":
	case json.Unmarshal(b.Content, &s) == nil:
		if s != "" {
			res.Content = []cBlock{{Text: &s}}
		}
	default:
		var blocks []msgBlock
		if err := json.Unmarshal(b.Content, &blocks); err != nil {
			return nil, &translate.Error{Message: "a tool result's content doesn't read"}
		}
		for _, c := range blocks {
			switch c.Type {
			case "text":
				if c.Text != "" {
					text := c.Text
					res.Content = append(res.Content, cBlock{Text: &text})
				}
			case "image":
				img, err := image(c.Source)
				if err != nil {
					return nil, err
				}
				res.Content = append(res.Content, cBlock{Image: img})
			case "document":
				doc, err := document(c, len(res.Content))
				if err != nil {
					return nil, err
				}
				res.Content = append(res.Content, cBlock{Document: doc})
			default:
				return nil, &translate.Error{Message: "a " + c.Type + " block in a tool result"}
			}
		}
	}
	if len(res.Content) == 0 {
		res.Content = []cBlock{{JSON: map[string]any{}}} // Converse takes no empty result
	}
	return res, nil
}

// tools are the request's tools and choice. A conversation that used
// tools needs them named even when the request offers none (Converse
// refuses tool blocks without a toolConfig): those used are declared
// open-ended.
func (t *translator) tools(in []msgTool, choice *msgToolChoice, msgs []cMessage) (*cToolConfig, error) {
	cfg := &cToolConfig{}
	declared := map[string]bool{}
	for _, tool := range in {
		if tool.Type != "" && tool.Type != "custom" {
			return nil, &translate.Error{Message: "the server tool " + tool.Type + " (Converse runs only the client's tools)"}
		}
		schema := tool.InputSchema
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		cfg.Tools = append(cfg.Tools, cTool{ToolSpec: &cToolSpec{Name: tool.Name, Description: tool.Description,
			InputSchema: cToolJSON{JSON: schema}}})
		declared[tool.Name] = true
		if cp := t.point(tool.CacheControl, "tools"); cp != nil {
			cfg.Tools = append(cfg.Tools, cTool{CachePoint: cp})
		}
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.ToolUse != nil && !declared[b.ToolUse.Name] {
				declared[b.ToolUse.Name] = true
				cfg.Tools = append(cfg.Tools, cTool{ToolSpec: &cToolSpec{Name: b.ToolUse.Name,
					InputSchema: cToolJSON{JSON: json.RawMessage(`{"type":"object"}`)}}})
			}
		}
	}
	if len(cfg.Tools) == 0 {
		if choice != nil && choice.Type != "" && choice.Type != "auto" && choice.Type != "none" {
			return nil, &translate.Error{Message: "a tool choice without tools"}
		}
		return nil, nil
	}
	if choice != nil {
		switch choice.Type {
		case "", "auto":
		case "any":
			cfg.ToolChoice = map[string]any{"any": map[string]any{}}
		case "tool":
			cfg.ToolChoice = map[string]any{"tool": map[string]string{"name": choice.Name}}
		case "none":
			t.dropped["tool_choice"] = true // Converse can't forbid the tools it is given
		default:
			return nil, &translate.Error{Message: "the tool choice " + choice.Type}
		}
	}
	return cfg, nil
}

func base64Of(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
