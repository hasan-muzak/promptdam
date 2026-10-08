package translate

import (
	"bytes"
	"encoding/json"
	"strings"
)

// The shapes of the requests, as far as translation reads or writes them.

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
	Refusal    string          `json:"refusal"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Refusal  string `json:"refusal"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
	File *struct {
		FileData string `json:"file_data"`
		FileID   string `json:"file_id"`
		Filename string `json:"filename"`
	} `json:"file"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// msgBlock is a content block of the Messages API, read and written.
type msgBlock struct {
	Type      string          `json:"type"`
	Text      *string         `json:"text,omitempty"`
	Source    *msgSource      `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Title     string          `json:"title,omitempty"`
}

type msgSource struct {
	Type      string          `json:"type"`
	MediaType string          `json:"media_type,omitempty"`
	Data      string          `json:"data,omitempty"`
	URL       string          `json:"url,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type msgMessage struct {
	Role    string     `json:"role"`
	Content []msgBlock `json:"content"`
}

type msgTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type msgToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type msgOutputConfig struct {
	Effort string     `json:"effort,omitempty"`
	Format *msgFormat `json:"format,omitempty"`
}

type msgFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

// messagesRequest is the Messages request Chat Completions turn into.
type messagesRequest struct {
	Model         string           `json:"model"`
	MaxTokens     int              `json:"max_tokens"`
	System        []msgBlock       `json:"system,omitempty"`
	Messages      []msgMessage     `json:"messages"`
	Tools         []msgTool        `json:"tools,omitempty"`
	ToolChoice    *msgToolChoice   `json:"tool_choice,omitempty"`
	Temperature   *float64         `json:"temperature,omitempty"`
	TopP          *float64         `json:"top_p,omitempty"`
	StopSequences []string         `json:"stop_sequences,omitempty"`
	Stream        bool             `json:"stream,omitempty"`
	Metadata      *msgMetadata     `json:"metadata,omitempty"`
	OutputConfig  *msgOutputConfig `json:"output_config,omitempty"`
}

type msgMetadata struct {
	UserID string `json:"user_id"`
}

func text(s string) *string { return &s }

// jsonInstruction asks for JSON where Anthropic has no JSON mode: Chat's
// response_format json_object.
const jsonInstruction = "Answer with a single JSON object and nothing else."

// chatToMessages turns a Chat Completions request into a Messages one.
func chatToMessages(body []byte, o Options) ([]byte, []string, error) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil, nil, refuse("the request body is not a JSON object")
	}
	out := messagesRequest{Model: o.Model, MaxTokens: o.MaxTokens}
	if out.MaxTokens <= 0 {
		out.MaxTokens = DefaultMaxTokens
	}
	var dropped []string
	for k, v := range top {
		switch k {
		case "model", "messages", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stop", "stream",
			"stream_options", "tools", "tool_choice", "parallel_tool_calls", "response_format", "user", "n",
			"reasoning_effort":
		case "audio", "modalities":
			if !null(v) && !bytes.Equal(bytes.TrimSpace(v), []byte(`["text"]`)) {
				return nil, nil, refuse("Anthropic models don't answer in audio")
			}
		default:
			if !null(v) {
				dropped = append(dropped, k)
			}
		}
	}
	var n int
	if raw := top["n"]; !null(raw) && (json.Unmarshal(raw, &n) != nil || n > 1) {
		return nil, nil, refuse("n asks for several answers; Anthropic models give one")
	}
	for _, k := range []string{"max_completion_tokens", "max_tokens"} {
		if raw := top[k]; !null(raw) {
			var m int
			if json.Unmarshal(raw, &m) == nil && m > 0 {
				out.MaxTokens = m
				break
			}
		}
	}

	var msgs []chatMessage
	if json.Unmarshal(top["messages"], &msgs) != nil {
		return nil, nil, refuse("messages is not a list of messages")
	}
	add := func(role string, blocks ...msgBlock) {
		if len(blocks) == 0 {
			return
		}
		if l := len(out.Messages); l > 0 && out.Messages[l-1].Role == role {
			out.Messages[l-1].Content = append(out.Messages[l-1].Content, blocks...)
			return
		}
		out.Messages = append(out.Messages, msgMessage{Role: role, Content: blocks})
	}
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			s, err := chatText(m.Content)
			if err != nil {
				return nil, nil, err
			}
			if s != "" {
				out.System = append(out.System, msgBlock{Type: "text", Text: text(s)})
			}
		case "user":
			blocks, err := chatUserBlocks(m.Content)
			if err != nil {
				return nil, nil, err
			}
			add("user", blocks...)
		case "assistant":
			blocks, err := chatAssistantBlocks(m)
			if err != nil {
				return nil, nil, err
			}
			add("assistant", blocks...)
		case "tool":
			s, err := chatText(m.Content)
			if err != nil {
				return nil, nil, err
			}
			content, _ := marshal(s)
			add("user", msgBlock{Type: "tool_result", ToolUseID: toolID(m.ToolCallID), Content: content})
		default:
			return nil, nil, refuse("messages with the role " + m.Role + " have no Anthropic equivalent")
		}
	}

	if raw := top["tools"]; !null(raw) {
		var tools []chatTool
		if json.Unmarshal(raw, &tools) != nil {
			return nil, nil, refuse("tools is not a list of tools")
		}
		for _, t := range tools {
			if t.Type != "function" {
				return nil, nil, refuse("tools of type " + t.Type + " have no Anthropic equivalent")
			}
			schema := t.Function.Parameters
			if null(schema) {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			out.Tools = append(out.Tools, msgTool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: schema})
		}
	}
	if raw := top["tool_choice"]; !null(raw) {
		tc, err := chatToolChoice(raw)
		if err != nil {
			return nil, nil, err
		}
		out.ToolChoice = tc
	}
	var parallel *bool
	if raw := top["parallel_tool_calls"]; !null(raw) {
		_ = json.Unmarshal(raw, &parallel)
	}
	if parallel != nil && !*parallel && len(out.Tools) > 0 {
		if out.ToolChoice == nil {
			out.ToolChoice = &msgToolChoice{Type: "auto"}
		}
		if out.ToolChoice.Type != "none" {
			out.ToolChoice.DisableParallelToolUse = true
		}
	}

	if raw := top["temperature"]; !null(raw) {
		var t float64
		if json.Unmarshal(raw, &t) == nil {
			t = min(max(t, 0), 1) // Chat's range is 0–2, Messages' 0–1
			out.Temperature = &t
		}
	}
	if raw := top["top_p"]; !null(raw) {
		var p float64
		if json.Unmarshal(raw, &p) == nil {
			if out.Temperature != nil {
				// Recent Anthropic models take one of the two.
				dropped = append(dropped, "top_p")
			} else {
				out.TopP = &p
			}
		}
	}
	if raw := top["stop"]; !null(raw) {
		var one string
		if json.Unmarshal(raw, &one) == nil {
			out.StopSequences = []string{one}
		} else if json.Unmarshal(raw, &out.StopSequences) != nil {
			return nil, nil, refuse("stop is not a string or a list of strings")
		}
	}
	if raw := top["stream"]; !null(raw) {
		_ = json.Unmarshal(raw, &out.Stream)
	}
	if raw := top["user"]; !null(raw) {
		var u string
		if json.Unmarshal(raw, &u) == nil && u != "" {
			out.Metadata = &msgMetadata{UserID: u}
		}
	}
	if raw := top["reasoning_effort"]; !null(raw) {
		var effort string
		_ = json.Unmarshal(raw, &effort)
		switch {
		case !o.Reasoning:
			dropped = append(dropped, "reasoning_effort")
		case effort == "minimal" || effort == "none":
			out.OutputConfig = &msgOutputConfig{Effort: "low"}
		case effort != "":
			out.OutputConfig = &msgOutputConfig{Effort: effort}
		}
	}
	if raw := top["response_format"]; !null(raw) {
		var rf struct {
			Type       string `json:"type"`
			JSONSchema *struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		}
		if json.Unmarshal(raw, &rf) != nil {
			return nil, nil, refuse("response_format is not an object")
		}
		switch rf.Type {
		case "", "text":
		case "json_schema":
			if rf.JSONSchema == nil || null(rf.JSONSchema.Schema) {
				return nil, nil, refuse("response_format json_schema has no schema")
			}
			if out.OutputConfig == nil {
				out.OutputConfig = &msgOutputConfig{}
			}
			out.OutputConfig.Format = &msgFormat{Type: "json_schema", Schema: rf.JSONSchema.Schema}
		case "json_object":
			out.System = append(out.System, msgBlock{Type: "text", Text: text(jsonInstruction)})
		default:
			return nil, nil, refuse("response_format " + rf.Type + " has no Anthropic equivalent")
		}
	}
	b, err := marshal(out)
	return b, dropped, err
}

// chatText is the text of a Chat content: a string or text parts.
func chatText(raw json.RawMessage) (string, error) {
	if null(raw) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []chatPart
	if json.Unmarshal(raw, &parts) != nil {
		return "", refuse("a message's content is not text or a list of parts")
	}
	var b strings.Builder
	for i, p := range parts {
		if p.Type != "text" {
			return "", refuse("only text can be sent here, not " + p.Type)
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

// chatUserBlocks turns a user message's content into Messages blocks.
func chatUserBlocks(raw json.RawMessage) ([]msgBlock, error) {
	if null(raw) {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return []msgBlock{{Type: "text", Text: text(s)}}, nil
	}
	var parts []chatPart
	if json.Unmarshal(raw, &parts) != nil {
		return nil, refuse("a message's content is not text or a list of parts")
	}
	out := make([]msgBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				out = append(out, msgBlock{Type: "text", Text: text(p.Text)})
			}
		case "image_url":
			if p.ImageURL == nil || p.ImageURL.URL == "" {
				return nil, refuse("an image_url part has no url")
			}
			src := &msgSource{Type: "url", URL: p.ImageURL.URL}
			if mt, data, ok := dataURI(p.ImageURL.URL); ok {
				src = &msgSource{Type: "base64", MediaType: mt, Data: data}
			}
			out = append(out, msgBlock{Type: "image", Source: src})
		case "file":
			if p.File == nil || p.File.FileData == "" {
				return nil, refuse("files can be sent to Anthropic models inline (file_data) only, not by file_id")
			}
			mt, data, ok := dataURI(p.File.FileData)
			if !ok {
				return nil, refuse("a file's file_data is not a base64 data URL")
			}
			if mt != "application/pdf" {
				return nil, refuse("Anthropic models read PDF files, not " + mt)
			}
			out = append(out, msgBlock{Type: "document", Source: &msgSource{Type: "base64", MediaType: mt, Data: data},
				Title: p.File.Filename})
		default:
			return nil, refuse("content parts of type " + p.Type + " have no Anthropic equivalent")
		}
	}
	return out, nil
}

// chatAssistantBlocks turns an assistant message into text and tool_use
// blocks.
func chatAssistantBlocks(m chatMessage) ([]msgBlock, error) {
	var out []msgBlock
	if !null(m.Content) {
		var s string
		if json.Unmarshal(m.Content, &s) != nil {
			var parts []chatPart
			if json.Unmarshal(m.Content, &parts) != nil {
				return nil, refuse("an assistant message's content is not text or a list of parts")
			}
			var b strings.Builder
			for _, p := range parts {
				switch p.Type {
				case "text":
					b.WriteString(p.Text)
				case "refusal":
					b.WriteString(p.Refusal)
				default:
					return nil, refuse("assistant content parts of type " + p.Type + " have no Anthropic equivalent")
				}
			}
			s = b.String()
		}
		if s != "" {
			out = append(out, msgBlock{Type: "text", Text: text(s)})
		}
	}
	if m.Refusal != "" && len(out) == 0 {
		out = append(out, msgBlock{Type: "text", Text: text(m.Refusal)})
	}
	for _, tc := range m.ToolCalls {
		if tc.Type != "" && tc.Type != "function" {
			return nil, refuse("tool calls of type " + tc.Type + " have no Anthropic equivalent")
		}
		out = append(out, msgBlock{Type: "tool_use", ID: toolID(tc.ID), Name: tc.Function.Name, Input: toolInput(tc.Function.Arguments)})
	}
	return out, nil
}

// toolInput is a tool call's arguments as a JSON object: a model's
// arguments that aren't one are kept whole under "arguments".
func toolInput(args string) json.RawMessage {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed), &obj) == nil && obj != nil {
		return json.RawMessage(trimmed)
	}
	wrapped, _ := marshal(map[string]string{"arguments": args})
	return wrapped
}

// toolID makes a tool call's ID one Anthropic accepts (letters, digits,
// _ and -), the same way for a call and its result.
func toolID(id string) string {
	if id == "" {
		return "call_0"
	}
	ok := true
	for i := 0; i < len(id) && ok; i++ {
		c := id[i]
		ok = c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	if ok {
		return id
	}
	b := []byte(id)
	for i, c := range b {
		if c != '_' && c != '-' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			b[i] = '_'
		}
	}
	return string(b)
}

func chatToolChoice(raw json.RawMessage) (*msgToolChoice, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case "none":
			return &msgToolChoice{Type: "none"}, nil
		case "auto":
			return &msgToolChoice{Type: "auto"}, nil
		case "required":
			return &msgToolChoice{Type: "any"}, nil
		}
		return nil, refuse("tool_choice " + s + " has no Anthropic equivalent")
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &obj) != nil || obj.Type != "function" || obj.Function.Name == "" {
		return nil, refuse("tool_choice must be none, auto, required or a function")
	}
	return &msgToolChoice{Type: "tool", Name: obj.Function.Name}, nil
}

// chatRequest is the Chat Completions request Messages turn into.
type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []chatOut       `json:"messages"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                []string        `json:"stop,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *streamOptions  `json:"stream_options,omitempty"`
	Tools               []chatToolOut   `json:"tools,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      *responseFormat `json:"response_format,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	User                string          `json:"user,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatOut struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatPartOut struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
	File     *fileOut  `json:"file,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type fileOut struct {
	Filename string `json:"filename"`
	FileData string `json:"file_data"`
}

type chatToolOut struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type responseFormat struct {
	Type       string `json:"type"`
	JSONSchema struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
		Strict bool            `json:"strict"`
	} `json:"json_schema"`
}

// messagesToChat turns a Messages request into a Chat Completions one.
func messagesToChat(body []byte, o Options) ([]byte, []string, error) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil, nil, refuse("the request body is not a JSON object")
	}
	out := chatRequest{Model: o.Model}
	var dropped []string
	for k, v := range top {
		switch k {
		case "model", "max_tokens", "system", "messages", "tools", "tool_choice", "temperature", "top_p", "stop_sequences",
			"stream", "metadata", "thinking", "output_config":
		case "mcp_servers", "container":
			if !null(v) {
				return nil, nil, refuse(k + " runs on Anthropic's side and has no OpenAI equivalent")
			}
		default:
			if !null(v) {
				dropped = append(dropped, k)
			}
		}
	}
	var maxTokens int
	_ = json.Unmarshal(top["max_tokens"], &maxTokens)
	if o.Kind == "openai" {
		out.MaxCompletionTokens = maxTokens
	} else {
		out.MaxTokens = maxTokens
	}

	if raw := top["system"]; !null(raw) {
		s, err := messagesText(raw)
		if err != nil {
			return nil, nil, err
		}
		if s != "" {
			out.Messages = append(out.Messages, chatOut{Role: "system", Content: s})
		}
	}
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(top["messages"], &msgs) != nil {
		return nil, nil, refuse("messages is not a list of messages")
	}
	if l := len(msgs); l > 0 && msgs[l-1].Role == "assistant" {
		return nil, nil, refuse("the request ends with an assistant message (a prefill), which OpenAI-format models don't continue")
	}
	for _, m := range msgs {
		blocks, err := messagesBlocks(m.Content)
		if err != nil {
			return nil, nil, err
		}
		switch m.Role {
		case "user":
			msgs, err := userToChat(blocks)
			if err != nil {
				return nil, nil, err
			}
			out.Messages = append(out.Messages, msgs...)
		case "assistant":
			msg, thinking, err := assistantToChat(blocks)
			if err != nil {
				return nil, nil, err
			}
			if thinking {
				dropped = append(dropped, "thinking")
			}
			if msg != nil {
				out.Messages = append(out.Messages, *msg)
			}
		default:
			return nil, nil, refuse("messages with the role " + m.Role + " have no OpenAI equivalent")
		}
	}

	if raw := top["tools"]; !null(raw) {
		var tools []struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if json.Unmarshal(raw, &tools) != nil {
			return nil, nil, refuse("tools is not a list of tools")
		}
		for _, t := range tools {
			if t.Type != "" && t.Type != "custom" {
				return nil, nil, refuse("Anthropic's " + t.Type + " tool has no OpenAI equivalent")
			}
			var ct chatToolOut
			ct.Type = "function"
			ct.Function.Name, ct.Function.Description, ct.Function.Parameters = t.Name, t.Description, t.InputSchema
			if null(ct.Function.Parameters) {
				ct.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			out.Tools = append(out.Tools, ct)
		}
	}
	if raw := top["tool_choice"]; !null(raw) {
		var tc msgToolChoice
		if json.Unmarshal(raw, &tc) != nil {
			return nil, nil, refuse("tool_choice is not an object")
		}
		switch tc.Type {
		case "auto":
			out.ToolChoice = "auto"
		case "any":
			out.ToolChoice = "required"
		case "none":
			out.ToolChoice = "none"
		case "tool":
			out.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
		default:
			return nil, nil, refuse("tool_choice " + tc.Type + " has no OpenAI equivalent")
		}
		if tc.DisableParallelToolUse && len(out.Tools) > 0 {
			no := false
			out.ParallelToolCalls = &no
		}
	}
	if raw := top["temperature"]; !null(raw) {
		var t float64
		if json.Unmarshal(raw, &t) == nil {
			out.Temperature = &t
		}
	}
	if raw := top["top_p"]; !null(raw) {
		var p float64
		if json.Unmarshal(raw, &p) == nil {
			out.TopP = &p
		}
	}
	if raw := top["stop_sequences"]; !null(raw) {
		if json.Unmarshal(raw, &out.Stop) != nil {
			return nil, nil, refuse("stop_sequences is not a list of strings")
		}
	}
	if raw := top["stream"]; !null(raw) {
		_ = json.Unmarshal(raw, &out.Stream)
	}
	if out.Stream {
		// The translated stream reads the usage from OpenAI's last chunk.
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if raw := top["metadata"]; !null(raw) {
		var md msgMetadata
		if json.Unmarshal(raw, &md) == nil {
			out.User = md.UserID
		}
	}
	if raw := top["thinking"]; !null(raw) {
		var th struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		}
		_ = json.Unmarshal(raw, &th)
		switch {
		case th.Type == "disabled" || th.Type == "":
		case !o.Reasoning:
			dropped = append(dropped, "thinking")
		case th.Type == "enabled" && th.BudgetTokens < 4096:
			out.ReasoningEffort = "low"
		case th.Type == "enabled" && th.BudgetTokens < 16384:
			out.ReasoningEffort = "medium"
		case th.Type == "enabled":
			out.ReasoningEffort = "high"
		default: // adaptive
			out.ReasoningEffort = "medium"
		}
	}
	if raw := top["output_config"]; !null(raw) {
		var oc msgOutputConfig
		if json.Unmarshal(raw, &oc) != nil {
			return nil, nil, refuse("output_config is not an object")
		}
		if oc.Effort != "" {
			switch {
			case !o.Reasoning:
				dropped = append(dropped, "output_config.effort")
			case oc.Effort == "xhigh" || oc.Effort == "max":
				out.ReasoningEffort = "high"
			default:
				out.ReasoningEffort = oc.Effort
			}
		}
		if oc.Format != nil {
			if oc.Format.Type != "json_schema" || null(oc.Format.Schema) {
				return nil, nil, refuse("output_config.format must be a json_schema with a schema")
			}
			rf := &responseFormat{Type: "json_schema"}
			rf.JSONSchema.Name, rf.JSONSchema.Schema, rf.JSONSchema.Strict = "output", oc.Format.Schema, true
			out.ResponseFormat = rf
		}
	}
	b, err := marshal(out)
	return b, dropped, err
}

// messagesText is the text of a Messages content: a string or text blocks.
func messagesText(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	blocks, err := messagesBlocks(raw)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, bl := range blocks {
		if bl.Type != "text" || bl.Text == nil {
			return "", refuse("only text can be sent here, not " + bl.Type)
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(*bl.Text)
	}
	return b.String(), nil
}

// messagesBlocks reads a Messages content: a string is one text block.
func messagesBlocks(raw json.RawMessage) ([]msgBlock, error) {
	if null(raw) {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []msgBlock{{Type: "text", Text: text(s)}}, nil
	}
	var blocks []msgBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, refuse("a message's content is not text or a list of blocks")
	}
	return blocks, nil
}

// userToChat turns a user turn into Chat messages: its tool results first,
// one tool message each, then the rest as a user message.
func userToChat(blocks []msgBlock) ([]chatOut, error) {
	var out []chatOut
	var parts []chatPartOut
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			inner, err := messagesBlocks(b.Content)
			if err != nil {
				return nil, err
			}
			var s strings.Builder
			if b.IsError {
				s.WriteString("Error: ")
			}
			var images []chatPartOut
			texts := 0
			for _, ib := range inner {
				switch ib.Type {
				case "text":
					if ib.Text == nil {
						continue
					}
					if texts > 0 {
						s.WriteByte('\n')
					}
					s.WriteString(*ib.Text)
					texts++
				case "image":
					p, err := imagePart(ib.Source)
					if err != nil {
						return nil, err
					}
					images = append(images, p)
				default:
					return nil, refuse("tool results holding " + ib.Type + " blocks have no OpenAI equivalent")
				}
			}
			out = append(out, chatOut{Role: "tool", ToolCallID: b.ToolUseID, Content: s.String()})
			if len(images) > 0 {
				// OpenAI's tool messages hold text only: the images follow in
				// the user's message, introduced.
				parts = append(parts, chatPartOut{Type: "text", Text: "The result of tool call " + b.ToolUseID + " included these images:"})
				parts = append(parts, images...)
			}
		case "text":
			if b.Text != nil && *b.Text != "" {
				parts = append(parts, chatPartOut{Type: "text", Text: *b.Text})
			}
		case "image":
			p, err := imagePart(b.Source)
			if err != nil {
				return nil, err
			}
			parts = append(parts, p)
		case "document":
			p, err := documentPart(b)
			if err != nil {
				return nil, err
			}
			parts = append(parts, p)
		default:
			return nil, refuse("content blocks of type " + b.Type + " have no OpenAI equivalent")
		}
	}
	switch {
	case len(parts) == 1 && parts[0].Type == "text":
		out = append(out, chatOut{Role: "user", Content: parts[0].Text})
	case len(parts) > 0:
		out = append(out, chatOut{Role: "user", Content: parts})
	}
	return out, nil
}

func imagePart(src *msgSource) (chatPartOut, error) {
	switch {
	case src == nil:
		return chatPartOut{}, refuse("an image block has no source")
	case src.Type == "base64":
		return chatPartOut{Type: "image_url", ImageURL: &imageURL{URL: "data:" + src.MediaType + ";base64," + src.Data}}, nil
	case src.Type == "url":
		return chatPartOut{Type: "image_url", ImageURL: &imageURL{URL: src.URL}}, nil
	}
	return chatPartOut{}, refuse("images from a " + src.Type + " source have no OpenAI equivalent")
}

func documentPart(b msgBlock) (chatPartOut, error) {
	src := b.Source
	switch {
	case src == nil:
		return chatPartOut{}, refuse("a document block has no source")
	case src.Type == "base64" && src.MediaType == "application/pdf":
		name := b.Title
		if name == "" {
			name = "document.pdf"
		}
		return chatPartOut{Type: "file", File: &fileOut{Filename: name, FileData: "data:application/pdf;base64," + src.Data}}, nil
	case src.Type == "text":
		return chatPartOut{Type: "text", Text: src.Data}, nil
	case src.Type == "content":
		s, err := messagesText(src.Content)
		if err != nil {
			return chatPartOut{}, err
		}
		return chatPartOut{Type: "text", Text: s}, nil
	}
	return chatPartOut{}, refuse("documents from a " + src.Type + " source have no OpenAI equivalent")
}

// assistantToChat turns an assistant turn into a Chat message (nil when it
// holds nothing to send). thinking reports thinking blocks left out.
func assistantToChat(blocks []msgBlock) (msg *chatOut, thinking bool, err error) {
	var s strings.Builder
	var calls []chatToolCall
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != nil {
				s.WriteString(*b.Text)
			}
		case "tool_use":
			args := string(b.Input)
			if null(b.Input) {
				args = "{}"
			}
			calls = append(calls, chatToolCall{ID: b.ID, Type: "function", Function: chatFunction{Name: b.Name, Arguments: args}})
		case "thinking", "redacted_thinking":
			thinking = true
		default:
			return nil, false, refuse("assistant blocks of type " + b.Type + " have no OpenAI equivalent")
		}
	}
	if s.Len() == 0 && len(calls) == 0 {
		return nil, thinking, nil
	}
	m := &chatOut{Role: "assistant", ToolCalls: calls}
	if s.Len() > 0 {
		m.Content = s.String()
	}
	return m, thinking, nil
}
