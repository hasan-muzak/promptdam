package gemini

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// The Chat Completions request a Gemini one becomes.
type chatRequest struct {
	Model            string         `json:"model"`
	Messages         []chatMessage  `json:"messages"`
	Tools            []chatTool     `json:"tools,omitempty"`
	ToolChoice       any            `json:"tool_choice,omitempty"`
	Temperature      *float64       `json:"temperature,omitempty"`
	TopP             *float64       `json:"top_p,omitempty"`
	MaxTokens        *int64         `json:"max_tokens,omitempty"`
	Stop             []string       `json:"stop,omitempty"`
	N                *int64         `json:"n,omitempty"`
	Seed             *int64         `json:"seed,omitempty"`
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	ResponseFormat   any            `json:"response_format,omitempty"`
	ReasoningEffort  string         `json:"reasoning_effort,omitempty"`
	Stream           bool           `json:"stream,omitempty"`
	StreamOptions    *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"` // a string, or []chatPart
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatPart struct {
	Type       string     `json:"type"`
	Text       string     `json:"text,omitempty"`
	ImageURL   *chatURL   `json:"image_url,omitempty"`
	File       *chatFile  `json:"file,omitempty"`
	InputAudio *chatAudio `json:"input_audio,omitempty"`
}

type chatURL struct {
	URL string `json:"url"`
}

type chatFile struct {
	FileData string `json:"file_data"`
	Filename string `json:"filename,omitempty"`
}

type chatAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
	Extra    *chatExtras  `json:"extra_content,omitempty"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatExtras carries Gemini 3's thought signature through Google's
// OpenAI-compatible API.
type chatExtras struct {
	Google struct {
		ThoughtSignature string `json:"thought_signature,omitempty"`
	} `json:"google"`
}

func signature(s string) *chatExtras {
	if s == "" {
		return nil
	}
	e := &chatExtras{}
	e.Google.ThoughtSignature = s
	return e
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatToolSpec `json:"function"`
}

type chatToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// serverTools are Google's own tools, which run at Google.
var serverTools = map[string]string{
	"googleSearch": "Google Search grounding", "googleSearchRetrieval": "Google Search grounding",
	"enterpriseWebSearch": "Google's web search", "codeExecution": "code execution", "urlContext": "URL context",
	"fileSearch": "file search", "googleMaps": "Google Maps grounding", "computerUse": "computer use",
	"mcpServers": "Google's MCP servers", "retrieval": "Vertex retrieval",
}

// Request turns a generateContent request for model (the path names it:
// the body doesn't) into a Chat Completions one, streamed with its usage
// when stream is set. dropped lists the fields left out; a request Chat
// can't express is refused (*translate.Error).
func Request(body []byte, model string, stream bool) (out []byte, dropped []string, err error) {
	top, ok := readObj(body)
	if !ok || top == nil {
		return nil, nil, refuse("the request body isn't a JSON object")
	}
	req := chatRequest{Model: model, Stream: stream}
	if stream {
		req.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	for _, k := range top.keys() {
		switch k {
		case "contents", "systemInstruction", "tools", "toolConfig", "generationConfig", "safetySettings", "model":
		case "cachedContent":
			if top.has(k) {
				return nil, nil, refuse("Google's context cache (cachedContent) lives at Google under its own key; " +
					"send the content itself: Promptdam caches prompts on its own")
			}
		case "continuationToken":
			if top.has(k) {
				return nil, nil, refuse("continuation tokens")
			}
		default:
			if top.has(k) {
				dropped = append(dropped, k)
			}
		}
	}
	if raw := top.get("systemInstruction"); !null(raw) {
		s, err := systemText(raw)
		if err != nil {
			return nil, nil, err
		}
		if s != "" {
			req.Messages = append(req.Messages, chatMessage{Role: "system", Content: s})
		}
	}
	contents, ok := top.list("contents")
	if !ok {
		return nil, nil, refuse("contents isn't a list of contents")
	}
	msgs, err := messages(contents)
	if err != nil {
		return nil, nil, err
	}
	req.Messages = append(req.Messages, msgs...)
	if len(req.Messages) == 0 {
		return nil, nil, refuse("the request has no contents")
	}
	if req.Tools, err = tools(top); err != nil {
		return nil, nil, err
	}
	d, err := toolConfig(top, &req)
	if err != nil {
		return nil, nil, err
	}
	dropped = append(dropped, d...)
	if d, err = generationConfig(top, &req); err != nil {
		return nil, nil, err
	}
	dropped = append(dropped, d...)
	if d, err = safety(top); err != nil {
		return nil, nil, err
	}
	dropped = append(dropped, d...)
	if out, err = marshal(req); err != nil {
		return nil, nil, err
	}
	slices.Sort(dropped)
	return out, slices.Compact(dropped), nil
}

// systemText is the system instruction's text: a Content's text parts, or
// a string.
func systemText(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	c, ok := readObj(raw)
	if !ok {
		return "", refuse("systemInstruction isn't a Content")
	}
	parts, ok := c.list("parts")
	if !ok {
		return "", refuse("systemInstruction's parts aren't a list")
	}
	var b strings.Builder
	for _, p := range parts {
		switch {
		case p.has("text"):
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.str("text"))
		case !empty(p):
			return "", refuse("a system instruction of anything but text")
		}
	}
	return b.String(), nil
}

// empty reports a part with nothing but metadata (a thought signature).
func empty(p obj) bool {
	for _, k := range p.keys() {
		switch k {
		case "thought", "thoughtSignature", "partMetadata", "mediaResolution", "videoMetadata":
		default:
			if p.has(k) {
				return false
			}
		}
	}
	return true
}

// messages are the conversation. A model turn's function calls get ids
// when Gemini sent none (by turn and place, so the same history gets the
// same ids each time), and the function responses after it are matched to
// them by id, else by name in order.
func messages(contents []obj) ([]chatMessage, error) {
	var out []chatMessage
	pending := map[string][]string{} // a name's calls not yet answered, by id
	for turn, c := range contents {
		parts, ok := c.list("parts")
		if !ok {
			return nil, refuse("a content's parts aren't a list")
		}
		if c.str("role") == "model" {
			m, err := modelTurn(parts, turn)
			if err != nil {
				return nil, err
			}
			clear(pending)
			for _, tc := range m.ToolCalls {
				pending[tc.Function.Name] = append(pending[tc.Function.Name], tc.ID)
			}
			if m.Content != nil || len(m.ToolCalls) > 0 {
				out = append(out, m)
			}
			continue
		}
		// The user's turn (or Gemini's older "function" role): its function
		// responses answer the calls before it, so they come first.
		var user []chatPart
		for _, p := range parts {
			if p.has("functionResponse") {
				m, err := functionResponse(p, pending)
				if err != nil {
					return nil, err
				}
				out = append(out, m)
				continue
			}
			cp, err := userPart(p)
			if err != nil {
				return nil, err
			}
			if cp != nil {
				user = append(user, *cp)
			}
		}
		switch {
		case len(user) == 1 && user[0].Type == "text":
			out = append(out, chatMessage{Role: "user", Content: user[0].Text})
		case len(user) > 0:
			out = append(out, chatMessage{Role: "user", Content: user})
		}
	}
	return out, nil
}

// modelTurn is a model's turn: its text and function calls. Thought
// summaries are left out (their signatures carry the model's state).
func modelTurn(parts []obj, turn int) (chatMessage, error) {
	m := chatMessage{Role: "assistant"}
	var text strings.Builder
	hasText := false
	for _, p := range parts {
		var thought bool
		_ = json.Unmarshal(p.get("thought"), &thought)
		switch {
		case thought:
		case p.has("text"):
			text.WriteString(p.str("text"))
			hasText = true
		case p.has("functionCall"):
			fc, ok := readObj(p.get("functionCall"))
			if !ok {
				return m, refuse("a functionCall isn't an object")
			}
			id := fc.str("id")
			if id == "" {
				id = "call_" + strconv.Itoa(turn) + "_" + strconv.Itoa(len(m.ToolCalls))
			}
			args := fc.get("args")
			if null(args) {
				args = json.RawMessage(`{}`)
			}
			m.ToolCalls = append(m.ToolCalls, chatToolCall{ID: id, Type: "function",
				Function: chatFunction{Name: fc.str("name"), Arguments: string(args)}, Extra: signature(p.str("thoughtSignature"))})
		case p.has("executableCode"), p.has("codeExecutionResult"), p.has("toolCall"), p.has("toolResponse"):
			return m, refuse("code execution and Google's server tools run at Google only")
		case !empty(p):
			return m, refuse("a model turn with anything but text and function calls")
		}
	}
	if hasText {
		m.Content = text.String()
	}
	return m, nil
}

// functionResponse is a tool message answering the call it names, or the
// first of its name not yet answered.
func functionResponse(p obj, pending map[string][]string) (chatMessage, error) {
	fr, ok := readObj(p.get("functionResponse"))
	if !ok || fr == nil {
		return chatMessage{}, refuse("a functionResponse isn't an object")
	}
	if fr.has("parts") {
		return chatMessage{}, refuse("a function response carrying files (parts)")
	}
	name, id := fr.str("name"), fr.str("id")
	queue := pending[name]
	switch i := slices.Index(queue, id); {
	case id != "" && i >= 0:
		pending[name] = slices.Delete(queue, i, i+1)
	case id != "":
	case len(queue) > 0:
		id, pending[name] = queue[0], queue[1:]
	default:
		id = "call_" + name
	}
	content := fr.get("response")
	if null(content) {
		content = json.RawMessage(`{}`)
	}
	return chatMessage{Role: "tool", ToolCallID: id, Content: string(content)}, nil
}

// userPart is a part of the user's turn as a Chat part (nil for one that
// carries nothing).
func userPart(p obj) (*chatPart, error) {
	var thought bool
	_ = json.Unmarshal(p.get("thought"), &thought)
	switch {
	case thought:
		return nil, nil
	case p.has("text"):
		if p.str("text") == "" {
			return nil, nil
		}
		return &chatPart{Type: "text", Text: p.str("text")}, nil
	case p.has("inlineData"):
		d, ok := readObj(p.get("inlineData"))
		if !ok || d == nil {
			return nil, refuse("an inlineData isn't an object")
		}
		return inline(d.str("mimeType"), d.str("data"), d.str("displayName"))
	case p.has("fileData"):
		f, ok := readObj(p.get("fileData"))
		if !ok || f == nil {
			return nil, refuse("a fileData isn't an object")
		}
		uri, mt := f.str("fileUri"), f.str("mimeType")
		switch {
		case strings.HasPrefix(uri, "https://generativelanguage.googleapis.com/") || strings.HasPrefix(uri, "gs://"):
			return nil, refuse("a file uploaded to Google (" + uri + "): only Google can read it; send it inline (inlineData)")
		case strings.HasPrefix(uri, "https://") && strings.HasPrefix(mt, "image/"):
			return &chatPart{Type: "image_url", ImageURL: &chatURL{URL: uri}}, nil
		}
		return nil, refuse("a file by URI of type " + mt + "; send it inline (inlineData)")
	case p.has("functionCall"):
		return nil, refuse("a function call in the user's turn")
	case p.has("executableCode"), p.has("codeExecutionResult"), p.has("toolCall"), p.has("toolResponse"):
		return nil, refuse("code execution and Google's server tools run at Google only")
	case empty(p):
		return nil, nil
	}
	return nil, refuse("a part of a kind Chat can't carry")
}

// inline is a file sent in the request: images and PDFs as Chat takes
// them, WAV and MP3 audio, text as text.
func inline(mt, data, name string) (*chatPart, error) {
	mt = strings.ToLower(strings.TrimSpace(mt))
	switch {
	case strings.HasPrefix(mt, "image/"):
		return &chatPart{Type: "image_url", ImageURL: &chatURL{URL: "data:" + mt + ";base64," + data}}, nil
	case mt == "application/pdf":
		if name == "" {
			name = "document.pdf"
		}
		return &chatPart{Type: "file", File: &chatFile{FileData: "data:application/pdf;base64," + data, Filename: name}}, nil
	case mt == "audio/wav" || mt == "audio/x-wav" || mt == "audio/wave":
		return &chatPart{Type: "input_audio", InputAudio: &chatAudio{Data: data, Format: "wav"}}, nil
	case mt == "audio/mp3" || mt == "audio/mpeg":
		return &chatPart{Type: "input_audio", InputAudio: &chatAudio{Data: data, Format: "mp3"}}, nil
	case strings.HasPrefix(mt, "text/"):
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, refuse("an inline " + mt + " file that isn't base64")
		}
		return &chatPart{Type: "text", Text: string(b)}, nil
	case strings.HasPrefix(mt, "video/"):
		return nil, refuse("video")
	}
	return nil, refuse("an inline file of type " + mt)
}

// tools are the request's function declarations; Google's own tools are
// refused.
func tools(top obj) ([]chatTool, error) {
	list, ok := top.list("tools")
	if !ok {
		return nil, refuse("tools isn't a list of tools")
	}
	var out []chatTool
	for _, t := range list {
		for _, k := range t.keys() {
			if !t.has(k) {
				continue
			}
			if what, ok := serverTools[k]; ok {
				return nil, refuse(what + " (" + k + ") runs at Google only")
			}
			if k != "functionDeclarations" {
				return nil, refuse("a tool of kind " + k)
			}
			decls, ok := t.list(k)
			if !ok {
				return nil, refuse("functionDeclarations isn't a list")
			}
			for _, d := range decls {
				spec := chatToolSpec{Name: d.str("name"), Description: d.str("description")}
				switch {
				case d.has("parametersJsonSchema"):
					spec.Parameters = d.get("parametersJsonSchema")
				case d.has("parameters"):
					s, err := jsonSchema(d.get("parameters"))
					if err != nil {
						return nil, refuse("the parameters of " + spec.Name + " don't read")
					}
					spec.Parameters = s
				}
				out = append(out, chatTool{Type: "function", Function: spec})
			}
		}
	}
	return out, nil
}

// toolConfig sets the tool choice: AUTO (and VALIDATED) leave it to the
// model, NONE forbids tools, ANY requires one; allowed names narrow the
// tools to them.
func toolConfig(top obj, req *chatRequest) (dropped []string, err error) {
	tc, ok := readObj(top.get("toolConfig"))
	if !ok {
		return nil, refuse("toolConfig isn't an object")
	}
	for _, k := range tc.keys() {
		if k != "functionCallingConfig" && tc.has(k) {
			dropped = append(dropped, "toolConfig."+k)
		}
	}
	fcc, ok := readObj(tc.get("functionCallingConfig"))
	if !ok {
		return nil, refuse("functionCallingConfig isn't an object")
	}
	if fcc == nil {
		return dropped, nil
	}
	var names []string
	_ = json.Unmarshal(fcc.get("allowedFunctionNames"), &names)
	only := func() {
		if len(names) == 0 {
			return
		}
		req.Tools = slices.DeleteFunc(req.Tools, func(t chatTool) bool { return !slices.Contains(names, t.Function.Name) })
	}
	switch strings.ToUpper(fcc.str("mode")) {
	case "NONE":
		if len(req.Tools) > 0 {
			req.ToolChoice = "none"
		}
	case "ANY":
		only()
		switch {
		case len(names) == 1 && len(req.Tools) == 1:
			req.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": names[0]}}
		case len(req.Tools) > 0:
			req.ToolChoice = "required"
		}
	case "VALIDATED":
		only()
	}
	return dropped, nil
}

// efforts are Gemini's thinking levels as reasoning efforts.
var efforts = map[string]string{"MINIMAL": "minimal", "LOW": "low", "MEDIUM": "medium", "HIGH": "high"}

// generationConfig sets sampling, the answer's limit and format, and the
// thinking effort.
func generationConfig(top obj, req *chatRequest) (dropped []string, err error) {
	gc, ok := readObj(top.get("generationConfig"))
	if !ok {
		return nil, refuse("generationConfig isn't an object")
	}
	for _, k := range gc.keys() {
		raw := gc.get(k)
		if null(raw) {
			continue
		}
		switch k {
		case "temperature":
			req.Temperature = number(raw)
		case "topP":
			req.TopP = number(raw)
		case "presencePenalty":
			req.PresencePenalty = number(raw)
		case "frequencyPenalty":
			req.FrequencyPenalty = number(raw)
		case "maxOutputTokens":
			req.MaxTokens = whole(raw)
		case "seed":
			req.Seed = whole(raw)
		case "candidateCount":
			if n := whole(raw); n != nil && *n > 1 {
				req.N = n
			}
		case "stopSequences":
			if json.Unmarshal(raw, &req.Stop) != nil {
				return nil, refuse("stopSequences isn't a list of strings")
			}
		case "responseModalities":
			var mods []string
			_ = json.Unmarshal(raw, &mods)
			for _, m := range mods {
				if !strings.EqualFold(m, "TEXT") {
					return nil, refuse("answers in " + strings.ToLower(m))
				}
			}
		case "responseMimeType", "responseSchema", "responseJsonSchema", "_responseJsonSchema", "thinkingConfig":
		default:
			dropped = append(dropped, "generationConfig."+k)
		}
	}
	if err := responseFormat(gc, req); err != nil {
		return nil, err
	}
	think, ok := readObj(gc.get("thinkingConfig"))
	if !ok {
		return nil, refuse("thinkingConfig isn't an object")
	}
	if think != nil {
		if e, ok := efforts[strings.ToUpper(think.str("thinkingLevel"))]; ok {
			req.ReasoningEffort = e
		}
		if b := whole(think.get("thinkingBudget")); b != nil && req.ReasoningEffort == "" {
			switch {
			case *b == 0:
				req.ReasoningEffort = "none"
			case *b < 0: // dynamic: the model's own
			case *b <= 1024:
				req.ReasoningEffort = "low"
			case *b <= 8192:
				req.ReasoningEffort = "medium"
			default:
				req.ReasoningEffort = "high"
			}
		}
		var include bool
		if json.Unmarshal(think.get("includeThoughts"), &include) == nil && include {
			dropped = append(dropped, "generationConfig.thinkingConfig.includeThoughts")
		}
	}
	return dropped, nil
}

// responseFormat asks for JSON: by JSON Schema, by Gemini's own schema
// (converted), or any JSON object.
func responseFormat(gc obj, req *chatRequest) error {
	mt := strings.ToLower(gc.str("responseMimeType"))
	schema := gc.get("responseJsonSchema")
	if null(schema) {
		schema = gc.get("_responseJsonSchema")
	}
	switch {
	case mt == "text/x.enum":
		return refuse("enum answers (text/x.enum)")
	case !null(schema):
	case gc.has("responseSchema"):
		s, err := jsonSchema(gc.get("responseSchema"))
		if err != nil {
			return refuse("responseSchema doesn't read")
		}
		schema = s
	case mt == "application/json":
		req.ResponseFormat = map[string]string{"type": "json_object"}
		return nil
	default:
		return nil
	}
	req.ResponseFormat = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "schema": schema}}
	return nil
}

// loosening are the safety thresholds no stricter than a model's default:
// left out, they change nothing that matters.
var loosening = []string{"", "BLOCK_NONE", "OFF", "HARM_BLOCK_THRESHOLD_UNSPECIFIED"}

// safety leaves out safety settings that only loosen Google's filters;
// stricter ones are refused, as Promptdam can't apply them to other
// models.
func safety(top obj) (dropped []string, err error) {
	settings, ok := top.list("safetySettings")
	if !ok {
		return nil, refuse("safetySettings isn't a list")
	}
	for _, s := range settings {
		if th := strings.ToUpper(s.str("threshold")); !slices.Contains(loosening, th) {
			return nil, refuse("a safety setting stricter than the model's default (" + s.str("category") + ": " + th +
				"): Promptdam can't apply it to every model; its firewall blocks what you set up there")
		}
	}
	if len(settings) > 0 {
		dropped = append(dropped, "safetySettings")
	}
	return dropped, nil
}

func number(raw json.RawMessage) *float64 {
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return &f
}

// whole is an integer, which protobuf's JSON may write as a string.
func whole(raw json.RawMessage) *int64 {
	if null(raw) {
		return nil
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return &n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return &v
		}
	}
	return nil
}
