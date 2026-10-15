package converse

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// The other way: Bedrock's runtime API as clients speak it (AWS's SDKs,
// Anthropic's AnthropicBedrock), a door to the gateway. Converse and
// InvokeModel requests become Messages at the edge; answers go back as
// Converse's JSON, AWS's event stream, and AWS's errors.

// Op is what a path of Bedrock's runtime API asks for.
type Op uint8

// Ops.
const (
	OpConverse Op = iota + 1
	OpConverseStream
	OpInvoke
	OpInvokeStream
	OpCountTokens
)

var ops = map[string]Op{
	"converse": OpConverse, "converse-stream": OpConverseStream, "invoke": OpInvoke,
	"invoke-with-response-stream": OpInvokeStream, "count-tokens": OpCountTokens,
}

// Stream reports an operation answered in AWS's event stream.
func (o Op) Stream() bool { return o == OpConverseStream || o == OpInvokeStream }

// ParsePath reads a runtime path, as sent (escaped), after the door's
// prefix: /model/{modelId}/{operation}. AWS's SDKs escape the id's ':' and
// '/' (an ARN's); Anthropic's leave ':' as it is. A doubled slash (an
// endpoint ending in one) is taken.
func ParsePath(escaped string) (model string, op Op, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimLeft(escaped, "/"), "model/")
	i := strings.LastIndexByte(rest, '/')
	if !found || i <= 0 {
		return "", 0, false
	}
	if op, ok = ops[rest[i+1:]]; !ok {
		return "", 0, false
	}
	model, err := url.PathUnescape(rest[:i])
	if err != nil || model == "" {
		return "", 0, false
	}
	return model, op, true
}

// Converted is a client's request as Messages.
type Converted struct {
	Body []byte
	// Betas are the Anthropic betas the request asked for, for the
	// anthropic-beta header.
	Betas   []string
	Dropped []string
	// NoMaxTokens marks a request that set no limit: Converse's is then
	// the model's own, Messages wants one.
	NoMaxTokens bool
}

// The Messages request a Converse one becomes.
type mRequest struct {
	Model         string            `json:"model"`
	MaxTokens     int64             `json:"max_tokens,omitempty"`
	System        []mBlock          `json:"system,omitempty"`
	Messages      []mMessage        `json:"messages"`
	Tools         []mTool           `json:"tools,omitempty"`
	ToolChoice    map[string]string `json:"tool_choice,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	TopP          *float64          `json:"top_p,omitempty"`
	TopK          json.RawMessage   `json:"top_k,omitempty"`
	StopSequences []string          `json:"stop_sequences,omitempty"`
	Thinking      json.RawMessage   `json:"thinking,omitempty"`
	OutputConfig  map[string]any    `json:"output_config,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
}

type mMessage struct {
	Role    string   `json:"role"`
	Content []mBlock `json:"content"`
}

type mBlock struct {
	Type         string          `json:"type"`
	Text         *string         `json:"text,omitempty"`
	Source       any             `json:"source,omitempty"` // a *mSource, or a search result's string
	Title        string          `json:"title,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      []mBlock        `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Thinking     *string         `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Data         string          `json:"data,omitempty"`
	CacheControl *mCache         `json:"cache_control,omitempty"`
}

type mSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type mCache struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type mTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	Strict       bool            `json:"strict,omitempty"`
	CacheControl *mCache         `json:"cache_control,omitempty"`
}

// The Converse request, as clients send it.
type inRequest struct {
	Messages        []inMessage `json:"messages"`
	System          []inBlock   `json:"system"`
	InferenceConfig *struct {
		MaxTokens     *int64   `json:"maxTokens"`
		Temperature   *float64 `json:"temperature"`
		TopP          *float64 `json:"topP"`
		StopSequences []string `json:"stopSequences"`
	} `json:"inferenceConfig"`
	ToolConfig *struct {
		Tools      []inTool `json:"tools"`
		ToolChoice *struct {
			Auto *struct{} `json:"auto"`
			Any  *struct{} `json:"any"`
			Tool *struct {
				Name string `json:"name"`
			} `json:"tool"`
		} `json:"toolChoice"`
	} `json:"toolConfig"`
	AdditionalModelRequestFields map[string]json.RawMessage `json:"additionalModelRequestFields"`
	OutputConfig                 *struct {
		TextFormat *struct {
			Type      string `json:"type"`
			Structure *struct {
				JSONSchema *struct {
					Schema string `json:"schema"`
				} `json:"jsonSchema"`
			} `json:"structure"`
		} `json:"textFormat"`
		Effort string `json:"effort"`
	} `json:"outputConfig"`
	RequestMetadata map[string]string `json:"requestMetadata"`
}

type inMessage struct {
	Role    string    `json:"role"`
	Content []inBlock `json:"content"`
}

type inBlock struct {
	Text       *string     `json:"text"`
	Image      *inMedia    `json:"image"`
	Document   *inDocument `json:"document"`
	ToolUse    *cToolUse   `json:"toolUse"`
	ToolResult *struct {
		ToolUseID string    `json:"toolUseId"`
		Content   []inBlock `json:"content"`
		Status    string    `json:"status"`
	} `json:"toolResult"`
	ReasoningContent *struct {
		ReasoningText *struct {
			Text      string `json:"text"`
			Signature string `json:"signature"`
		} `json:"reasoningText"`
		RedactedContent string `json:"redactedContent"`
	} `json:"reasoningContent"`
	CachePoint *struct {
		TTL string `json:"ttl"`
	} `json:"cachePoint"`
	CitationsContent *struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"citationsContent"`
	SearchResult *struct {
		Source  string `json:"source"`
		Title   string `json:"title"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"searchResult"`
	JSON         json.RawMessage `json:"json"`
	GuardContent json.RawMessage `json:"guardContent"`
	Video        json.RawMessage `json:"video"`
	Audio        json.RawMessage `json:"audio"`
	ToolAddition json.RawMessage `json:"toolAddition"`
	ToolRemoval  json.RawMessage `json:"toolRemoval"`
}

type inMedia struct {
	Format string   `json:"format"`
	Source inSource `json:"source"`
}

type inDocument struct {
	Format    string          `json:"format"`
	Name      string          `json:"name"`
	Source    inSource        `json:"source"`
	Citations json.RawMessage `json:"citations"`
}

type inSource struct {
	Bytes      string          `json:"bytes"`
	S3Location json.RawMessage `json:"s3Location"`
	Text       *string         `json:"text"`
	Content    []struct {
		Text string `json:"text"`
	} `json:"content"`
}

type inTool struct {
	ToolSpec *struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			JSON json.RawMessage `json:"json"`
		} `json:"inputSchema"`
		Strict bool `json:"strict"`
	} `json:"toolSpec"`
	SystemTool json.RawMessage `json:"systemTool"`
	CachePoint *struct {
		TTL string `json:"ttl"`
	} `json:"cachePoint"`
}

// FromConverse turns a Converse request for model (the path names it) into
// a Messages one, streamed when stream is set. What Messages can't carry
// (guardrails, files in S3, video, audio, Office documents, prompt
// management) is refused (*translate.Error); settings without an
// equivalent are listed.
func FromConverse(body []byte, model string, stream bool) (Converted, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return Converted{}, refuse("the request body isn't a JSON object")
	}
	var dropped []string
	for k, v := range top {
		if string(v) == "null" {
			continue
		}
		switch k {
		case "messages", "system", "inferenceConfig", "toolConfig", "additionalModelRequestFields", "outputConfig",
			"requestMetadata", "modelId":
		case "guardrailConfig":
			return Converted{}, refuse("Bedrock guardrails run at AWS only; Promptdam's firewall blocks what you set up there")
		case "promptVariables":
			return Converted{}, refuse("Bedrock prompt management (promptVariables)")
		default:
			dropped = append(dropped, k)
		}
	}
	var in inRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return Converted{}, refuse("the request doesn't read as Converse: " + err.Error())
	}
	c := Converted{}
	out := mRequest{Model: model, Stream: stream}
	if ic := in.InferenceConfig; ic != nil {
		out.Temperature, out.TopP, out.StopSequences = ic.Temperature, ic.TopP, ic.StopSequences
		if ic.MaxTokens != nil {
			out.MaxTokens = *ic.MaxTokens
		}
	}
	c.NoMaxTokens = out.MaxTokens == 0
	var err error
	if out.System, err = fromBlocks(in.System, true); err != nil {
		return Converted{}, err
	}
	for _, m := range in.Messages {
		blocks, err := fromBlocks(m.Content, false)
		if err != nil {
			return Converted{}, err
		}
		switch m.Role {
		case "user", "assistant":
			out.Messages = append(out.Messages, mMessage{Role: m.Role, Content: blocks})
		case "system":
			out.System = append(out.System, blocks...)
		default:
			return Converted{}, refuse("a message of role " + m.Role)
		}
	}
	if tc := in.ToolConfig; tc != nil {
		for _, t := range tc.Tools {
			switch {
			case t.ToolSpec != nil:
				schema := t.ToolSpec.InputSchema.JSON
				if len(schema) == 0 || string(schema) == "null" {
					schema = json.RawMessage(`{"type":"object"}`)
				}
				out.Tools = append(out.Tools, mTool{Name: t.ToolSpec.Name, Description: t.ToolSpec.Description,
					InputSchema: schema, Strict: t.ToolSpec.Strict})
			case t.CachePoint != nil:
				if n := len(out.Tools); n > 0 {
					out.Tools[n-1].CacheControl = cacheOf(t.CachePoint.TTL)
				}
			case len(t.SystemTool) > 0:
				return Converted{}, refuse("Bedrock's system tools run at AWS only")
			}
		}
		if ch := tc.ToolChoice; ch != nil {
			switch {
			case ch.Any != nil:
				out.ToolChoice = map[string]string{"type": "any"}
			case ch.Tool != nil:
				out.ToolChoice = map[string]string{"type": "tool", "name": ch.Tool.Name}
			case ch.Auto != nil:
				out.ToolChoice = map[string]string{"type": "auto"}
			}
		}
	}
	extra, betas, d := fields(in.AdditionalModelRequestFields)
	dropped = append(dropped, d...)
	c.Betas = betas
	out.TopK, out.Thinking = extra.topK, extra.thinking
	out.OutputConfig = extra.output
	if oc := in.OutputConfig; oc != nil {
		if out.OutputConfig == nil {
			out.OutputConfig = map[string]any{}
		}
		if oc.Effort != "" {
			out.OutputConfig["effort"] = oc.Effort
		}
		if tf := oc.TextFormat; tf != nil && tf.Structure != nil && tf.Structure.JSONSchema != nil {
			schema := json.RawMessage(tf.Structure.JSONSchema.Schema)
			if !json.Valid(schema) {
				return Converted{}, refuse("outputConfig's JSON schema doesn't read")
			}
			out.OutputConfig["format"] = map[string]any{"type": "json_schema", "schema": schema}
		}
		if len(out.OutputConfig) == 0 {
			out.OutputConfig = nil
		}
	}
	for k, v := range in.RequestMetadata {
		if k == "user_id" {
			out.Metadata = map[string]string{"user_id": v}
		} else {
			dropped = append(dropped, "requestMetadata")
		}
	}
	if c.Body, err = jsonOf(out); err != nil {
		return Converted{}, err
	}
	dropped = append(dropped, blockDrops(in)...)
	slices.Sort(dropped)
	c.Dropped = slices.Compact(dropped)
	return c, nil
}

// blockDrops lists what the blocks carry that Messages leaves out: a
// document's citations setting.
func blockDrops(in inRequest) []string {
	for _, m := range in.Messages {
		for _, b := range m.Content {
			if b.Document != nil && len(b.Document.Citations) > 0 && string(b.Document.Citations) != "null" {
				return []string{"document.citations"}
			}
		}
	}
	return nil
}

// extras are the Anthropic fields a request passes in
// additionalModelRequestFields.
type extras struct {
	topK, thinking json.RawMessage
	output         map[string]any
}

// fields reads additionalModelRequestFields: Claude's top_k, thinking,
// output_config and betas, and Nova's topK and reasoning effort; the rest
// is listed as dropped.
func fields(raw map[string]json.RawMessage) (e extras, betas, dropped []string) {
	for k, v := range raw {
		switch k {
		case "top_k":
			e.topK = v
		case "thinking":
			e.thinking = v
		case "anthropic_beta":
			_ = json.Unmarshal(v, &betas)
		case "anthropic_version":
		case "output_config":
			_ = json.Unmarshal(v, &e.output)
		case "inferenceConfig":
			var ic struct {
				TopK json.RawMessage `json:"topK"`
			}
			if json.Unmarshal(v, &ic) == nil && len(ic.TopK) > 0 {
				e.topK = ic.TopK
			}
		case "reasoningConfig":
			var rc struct {
				Effort string `json:"maxReasoningEffort"`
			}
			if json.Unmarshal(v, &rc) == nil && rc.Effort != "" {
				if e.output == nil {
					e.output = map[string]any{}
				}
				e.output["effort"] = rc.Effort
			}
		default:
			dropped = append(dropped, "additionalModelRequestFields."+k)
		}
	}
	return e, betas, dropped
}

func cacheOf(ttl string) *mCache {
	c := &mCache{Type: "ephemeral"}
	if ttl == "1h" {
		c.TTL = "1h"
	}
	return c
}

// fromBlocks turns Converse's content blocks into Messages', a cachePoint
// marking the block before it.
func fromBlocks(in []inBlock, system bool) ([]mBlock, error) {
	var out []mBlock
	for _, b := range in {
		if b.CachePoint != nil {
			if n := len(out); n > 0 {
				out[n-1].CacheControl = cacheOf(b.CachePoint.TTL)
			}
			continue
		}
		if system && b.Text == nil {
			if len(b.GuardContent) > 0 {
				return nil, refuse("Bedrock guardrails run at AWS only; Promptdam's firewall blocks what you set up there")
			}
			return nil, refuse("a system block other than text")
		}
		blocks, err := fromBlock(b)
		if err != nil {
			return nil, err
		}
		out = append(out, blocks...)
	}
	return out, nil
}

// fromBlock is one content block as Messages blocks.
func fromBlock(b inBlock) ([]mBlock, error) {
	switch {
	case b.Text != nil:
		return []mBlock{{Type: "text", Text: b.Text}}, nil
	case b.Image != nil:
		img, err := imageOf(b.Image)
		if err != nil {
			return nil, err
		}
		return []mBlock{img}, nil
	case b.Document != nil:
		doc, err := documentOf(b.Document)
		if err != nil {
			return nil, err
		}
		return []mBlock{doc}, nil
	case b.ToolUse != nil:
		input := b.ToolUse.Input
		if len(input) == 0 || string(input) == "null" {
			input = json.RawMessage(`{}`)
		}
		return []mBlock{{Type: "tool_use", ID: b.ToolUse.ToolUseID, Name: b.ToolUse.Name, Input: input}}, nil
	case b.ToolResult != nil:
		res := mBlock{Type: "tool_result", ToolUseID: b.ToolResult.ToolUseID, IsError: b.ToolResult.Status == "error"}
		for _, c := range b.ToolResult.Content {
			if len(c.JSON) > 0 && string(c.JSON) != "null" {
				s := compact(c.JSON)
				res.Content = append(res.Content, mBlock{Type: "text", Text: &s})
				continue
			}
			blocks, err := fromBlock(c)
			if err != nil {
				return nil, err
			}
			res.Content = append(res.Content, blocks...)
		}
		return []mBlock{res}, nil
	case b.ReasoningContent != nil:
		r := b.ReasoningContent
		if r.ReasoningText != nil {
			return []mBlock{{Type: "thinking", Thinking: &r.ReasoningText.Text, Signature: r.ReasoningText.Signature}}, nil
		}
		return []mBlock{{Type: "redacted_thinking", Data: r.RedactedContent}}, nil
	case b.CitationsContent != nil:
		var out []mBlock
		for _, c := range b.CitationsContent.Content {
			out = append(out, mBlock{Type: "text", Text: &c.Text})
		}
		return out, nil
	case b.SearchResult != nil:
		sr := mBlock{Type: "search_result", Source: b.SearchResult.Source, Title: b.SearchResult.Title}
		for _, c := range b.SearchResult.Content {
			sr.Content = append(sr.Content, mBlock{Type: "text", Text: &c.Text})
		}
		return []mBlock{sr}, nil
	case len(b.GuardContent) > 0:
		return nil, refuse("Bedrock guardrails run at AWS only; Promptdam's firewall blocks what you set up there")
	case len(b.Video) > 0:
		return nil, refuse("video")
	case len(b.Audio) > 0:
		return nil, refuse("audio")
	case len(b.ToolAddition) > 0 || len(b.ToolRemoval) > 0:
		return nil, refuse("tools added or removed mid-conversation")
	}
	return nil, refuse("a content block of a kind Messages can't carry")
}

// imageOf is an image sent in the request (Messages can't read S3).
func imageOf(img *inMedia) (mBlock, error) {
	if len(img.Source.S3Location) > 0 || img.Source.Bytes == "" {
		return mBlock{}, refuse("an image in S3; send its bytes")
	}
	return mBlock{Type: "image", Source: &mSource{Type: "base64", MediaType: "image/" + img.Format, Data: img.Source.Bytes}}, nil
}

// textFormats are the document formats Messages reads as text.
var textFormats = []string{"txt", "md", "csv", "html"}

// documentOf is a PDF, or a text document, as Messages reads them.
func documentOf(d *inDocument) (mBlock, error) {
	s := d.Source
	if len(s.S3Location) > 0 {
		return mBlock{}, refuse("a document in S3; send its bytes")
	}
	text := func(t string) mBlock {
		return mBlock{Type: "document", Title: d.Name, Source: &mSource{Type: "text", MediaType: "text/plain", Data: t}}
	}
	switch {
	case s.Text != nil:
		return text(*s.Text), nil
	case len(s.Content) > 0:
		var b strings.Builder
		for i, c := range s.Content {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(c.Text)
		}
		return text(b.String()), nil
	case d.Format == "pdf":
		return mBlock{Type: "document", Title: d.Name, Source: &mSource{Type: "base64", MediaType: "application/pdf", Data: s.Bytes}}, nil
	case slices.Contains(textFormats, d.Format):
		raw, err := base64.StdEncoding.DecodeString(s.Bytes)
		if err != nil {
			return mBlock{}, refuse("a document's bytes aren't base64")
		}
		return text(string(raw)), nil
	}
	return mBlock{}, refuse("a " + d.Format + " document (Messages reads PDFs and text)")
}

// FromInvoke turns InvokeModel's body for Claude (Messages with
// anthropic_version, the model in the path) into a Messages request. Other
// models' own bodies are refused: Converse calls every model.
func FromInvoke(body []byte, model string, stream bool) (Converted, error) {
	version, ok, err := wire.Field(body, "anthropic_version")
	if err != nil {
		return Converted{}, refuse("the request body isn't a JSON object")
	}
	if !ok || string(version) == "null" {
		return Converted{}, refuse("an InvokeModel body other than Claude's (anthropic_version); call this model through Converse")
	}
	c := Converted{}
	if raw, ok, _ := wire.Field(body, "anthropic_beta"); ok {
		_ = json.Unmarshal(raw, &c.Betas)
	}
	_, hasMax, _ := wire.Field(body, "max_tokens")
	c.NoMaxTokens = !hasMax
	for _, k := range []string{"anthropic_version", "anthropic_beta", "stream"} {
		if body, err = wire.DeleteField(body, k); err != nil {
			return Converted{}, refuse("the request body isn't a JSON object")
		}
	}
	m, _ := json.Marshal(model)
	if body, err = wire.SetField(body, "model", m); err != nil {
		return Converted{}, refuse("the request body isn't a JSON object")
	}
	if stream {
		if body, err = wire.SetField(body, "stream", []byte("true")); err != nil {
			return Converted{}, refuse("the request body isn't a JSON object")
		}
	}
	c.Body = body
	return c, nil
}

func refuse(msg string) error { return &translate.Error{Message: msg} }

// compact is JSON without its spaces.
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// jsonOf encodes v without escaping HTML characters, as providers write
// JSON.
func jsonOf(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// CountRequest turns Bedrock's count-tokens request (a Converse request in
// input.converse, or an InvokeModel body, base64, in
// input.invokeModel.body) into a Messages one.
func CountRequest(body []byte, model string) (Converted, error) {
	var in struct {
		Input struct {
			Converse    json.RawMessage `json:"converse"`
			InvokeModel *struct {
				Body string `json:"body"`
			} `json:"invokeModel"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return Converted{}, refuse("the request body isn't a count-tokens request")
	}
	switch {
	case len(in.Input.Converse) > 0 && string(in.Input.Converse) != "null":
		return FromConverse(in.Input.Converse, model, false)
	case in.Input.InvokeModel != nil:
		raw, err := base64.StdEncoding.DecodeString(in.Input.InvokeModel.Body)
		if err != nil {
			return Converted{}, refuse("input.invokeModel.body isn't base64")
		}
		return FromInvoke(raw, model, false)
	}
	return Converted{}, refuse("count-tokens takes input.converse or input.invokeModel")
}
