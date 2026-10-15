// Package fakeupstream is a stand-in LLM provider for tests and benchmarks.
// It speaks the OpenAI Chat Completions, Responses and Embeddings APIs and
// the Anthropic Messages API, streamed or not, answers after set delays and
// reports fixed token counts, so what the gateway adds can be measured and
// what it reads can be checked.
package fakeupstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// Token counts every answer reports. Anthropic's input excludes the cached
// tokens, which it reports apart.
const (
	PromptTokens     = 100
	CachedTokens     = 40 // OpenAI: part of PromptTokens
	CacheReadTokens  = 50 // Anthropic: on top of PromptTokens
	CacheWriteTokens = 25 // Anthropic: on top of PromptTokens
)

// Options shape the answers.
type Options struct {
	// TTFB delays the response headers.
	TTFB time.Duration
	// ChunkDelay is the pause before each streamed chunk.
	ChunkDelay time.Duration
	// Chunks is the content chunks of an answer, and its output tokens
	// (default 10).
	Chunks int
	// Reply, when set, writes the text of whole (not streamed) Chat
	// Completions and Messages answers, from the model and the request.
	Reply func(model string, body []byte) string
	// StreamReply streams Reply's text too, three characters a chunk.
	StreamReply bool
	// Backend and Cost make Chat Completions answers look like a router
	// of providers' (OpenRouter's): each names the backend that served it
	// ("provider") and its usage says what it cost.
	Backend string
	Cost    float64
	// Prompt is the prompt tokens Chat Completions usage reports (default
	// PromptTokens), CachedTokens of them read from the cache.
	Prompt int
	// RefuseMarkers refuses Chat Completions calls with cache breakpoints
	// (cache_control), as a provider that doesn't take them.
	RefuseMarkers bool
}

// routed adds Backend, Cost and Prompt to a Chat Completions answer or
// chunk.
func (s *Server) routed(c map[string]any) map[string]any {
	if s.opts.Backend != "" {
		c["provider"] = s.opts.Backend
	}
	if u, ok := c["usage"].(map[string]any); ok {
		if s.opts.Cost > 0 {
			u["cost"] = s.opts.Cost
		}
		if s.opts.Prompt > 0 {
			u["total_tokens"] = s.opts.Prompt + u["completion_tokens"].(int)
			u["prompt_tokens"] = s.opts.Prompt
		}
	}
	return c
}

// pieces are a streamed answer's chunks: Reply's text cut in three
// characters, else tok0 tok1….
func (s *Server) pieces(model string, body []byte) []string {
	if s.opts.Reply == nil || !s.opts.StreamReply {
		out := make([]string, s.opts.Chunks)
		for i := range out {
			out[i] = fmt.Sprintf("tok%d ", i)
		}
		return out
	}
	var out []string
	runes := []rune(s.opts.Reply(model, body))
	for len(runes) > 0 {
		n := min(3, len(runes))
		out, runes = append(out, string(runes[:n])), runes[n:]
	}
	return out
}

// Request is a call the server received: Path is the API's own path (a
// cloud's mapped to it), URLPath the one called.
type Request struct {
	Path    string
	URLPath string
	Header  http.Header
	Body    []byte
}

// cloudPath maps a call's URL path to the API path it stands for: a cloud
// account's root before it (Bedrock's and Foundry's /anthropic/v1 and
// /openai/v1, Vertex's /endpoints/openapi), or Claude on Vertex's
// rawPredict, whose model (and whether it streams) is in the URL.
func cloudPath(raw string) (path, model string, stream bool) {
	if i := strings.LastIndex(raw, "/publishers/anthropic/models/"); i >= 0 {
		model, method, _ := strings.Cut(raw[i+len("/publishers/anthropic/models/"):], ":")
		if model == "count-tokens" {
			return "/messages/count_tokens", "", false
		}
		return "/messages", model, method == "streamRawPredict"
	}
	if strings.Contains(raw, "/publishers/mistralai/models/") {
		return "/chat/completions", "", false // Mistral on Vertex: its chat body, model in it
	}
	for _, root := range []string{"/anthropic/v1", "/openai/v1", "/endpoints/openapi"} {
		if i := strings.Index(raw, root+"/"); i >= 0 {
			return raw[i+len(root):], "", false
		}
	}
	return strings.TrimPrefix(raw, "/v1"), "", false
}

// Server is the fake provider.
type Server struct {
	opts Options

	mu       sync.Mutex
	requests []Request
	failures []int // statuses the next calls answer with
}

// New returns a fake provider.
func New(opts Options) *Server {
	if opts.Chunks == 0 {
		opts.Chunks = 10
	}
	return &Server{opts: opts}
}

// Requests returns the calls received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Fail makes the next calls answer with these statuses, one each.
func (s *Server) Fail(statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, statuses...)
}

// ServeHTTP answers a call. Paths are those under a provider's base URL
// with or without /v1: /v1/chat/completions and /chat/completions alike;
// and the clouds' (cloudPath).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path, urlModel, urlStream := cloudPath(r.URL.Path)
	s.mu.Lock()
	s.requests = append(s.requests, Request{Path: path, URLPath: r.URL.Path, Header: r.Header.Clone(), Body: body})
	fail := 0
	if len(s.failures) > 0 {
		fail, s.failures = s.failures[0], s.failures[1:]
	}
	s.mu.Unlock()

	if !s.sleep(r, s.opts.TTFB) {
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(path, "/models") {
		writeJSON(w, map[string]any{"object": "list", "data": []any{}})
		return
	}
	if id, method, ok := converseCall(r.URL.Path); ok {
		s.converse(w, r, id, method == "converse-stream", body, fail)
		return
	}
	if fail != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fail)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"fake failure","type":"server_error","code":"%d"}}`, fail)
		return
	}
	var req struct {
		Model         string `json:"model"`
		Stream        bool   `json:"stream"`
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if json.Unmarshal(body, &req) != nil {
		req.Model = ""
	}
	if urlModel != "" { // Claude on Vertex: the model in the URL, never the body
		if req.Model != "" {
			http.Error(w, `{"error":{"message":"model: Extra inputs are not permitted","type":"invalid_request_error"}}`, http.StatusBadRequest)
			return
		}
		req.Model, req.Stream = urlModel, urlStream
	}
	if req.Model == "" {
		http.Error(w, `{"error":{"message":"bad request","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}
	switch path {
	case "/chat/completions":
		if s.opts.RefuseMarkers && bytes.Contains(body, []byte(`"cache_control"`)) {
			http.Error(w, `{"error":{"message":"Unrecognized request argument supplied: cache_control","type":"invalid_request_error"}}`,
				http.StatusBadRequest)
			return
		}
		if req.Stream {
			s.chatStream(w, r, req.Model, s.pieces(req.Model, body), req.StreamOptions != nil && req.StreamOptions.IncludeUsage)
		} else {
			c := s.routed(chatCompletion(req.Model, s.opts.Chunks))
			if s.opts.Reply != nil {
				c["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] = s.opts.Reply(req.Model, body)
			}
			writeJSON(w, c)
		}
	case "/responses":
		if req.Stream {
			s.responsesStream(w, r, req.Model)
		} else {
			writeJSON(w, response(req.Model, s.opts.Chunks))
		}
	case "/embeddings":
		writeJSON(w, map[string]any{
			"object": "list", "model": req.Model,
			"data":  []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}}},
			"usage": map[string]int{"prompt_tokens": PromptTokens, "total_tokens": PromptTokens},
		})
	case "/messages":
		if req.Stream {
			s.messagesStream(w, r, req.Model, s.pieces(req.Model, body))
		} else {
			m := message(req.Model, s.opts.Chunks)
			if s.opts.Reply != nil {
				m["content"].([]any)[0].(map[string]any)["text"] = s.opts.Reply(req.Model, body)
			}
			writeJSON(w, m)
		}
	case "/messages/count_tokens":
		writeJSON(w, map[string]int{"input_tokens": PromptTokens})
	default:
		http.NotFound(w, r)
	}
}

// sleep waits d; false when the client left first.
func (s *Server) sleep(r *http.Request, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", "req_fake")
	_ = json.NewEncoder(w).Encode(v)
}

// events writes a server-sent event stream, pausing before each event.
type events struct {
	s *Server
	w http.ResponseWriter
	r *http.Request
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) *events {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	return &events{s: s, w: w, r: r}
}

// send writes one event (name may be empty); false when the client left.
func (e *events) send(name string, data any) bool {
	if !e.s.sleep(e.r, e.s.opts.ChunkDelay) {
		return false
	}
	payload, ok := data.(string)
	if !ok {
		b, _ := json.Marshal(data)
		payload = string(b)
	}
	if name != "" {
		_, _ = fmt.Fprintf(e.w, "event: %s\n", name)
	}
	_, err := fmt.Fprintf(e.w, "data: %s\n\n", payload)
	e.w.(http.Flusher).Flush()
	return err == nil
}

func text(chunks int) string {
	var b strings.Builder
	for i := range chunks {
		fmt.Fprintf(&b, "tok%d ", i)
	}
	return b.String()
}

func openAIUsage(chunks int) map[string]any {
	return map[string]any{
		"prompt_tokens": PromptTokens, "completion_tokens": chunks, "total_tokens": PromptTokens + chunks,
		"prompt_tokens_details":     map[string]int{"cached_tokens": CachedTokens},
		"completion_tokens_details": map[string]int{"reasoning_tokens": 0},
	}
}

func chatCompletion(model string, chunks int) map[string]any {
	return map[string]any{
		"id": "chatcmpl-fake", "object": "chat.completion", "created": 1790000000, "model": model,
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "stop", "logprobs": nil,
			"message": map[string]any{"role": "assistant", "content": text(chunks), "refusal": nil},
		}},
		"usage": openAIUsage(chunks),
	}
}

func (s *Server) chatStream(w http.ResponseWriter, r *http.Request, model string, pieces []string, usage bool) {
	e := s.stream(w, r)
	chunk := func(delta map[string]any, finish any) map[string]any {
		c := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 1790000000, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish, "logprobs": nil}},
		}
		if usage {
			c["usage"] = nil
		}
		return s.routed(c)
	}
	if !e.send("", chunk(map[string]any{"role": "assistant", "content": ""}, nil)) {
		return
	}
	for _, p := range pieces {
		if !e.send("", chunk(map[string]any{"content": p}, nil)) {
			return
		}
	}
	if !e.send("", chunk(map[string]any{}, "stop")) {
		return
	}
	if usage {
		last := s.routed(map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 1790000000, "model": model,
			"choices": []any{}, "usage": openAIUsage(s.opts.Chunks),
		})
		if !e.send("", last) {
			return
		}
	}
	e.send("", "[DONE]")
}

func response(model string, chunks int) map[string]any {
	return map[string]any{
		"id": "resp_fake", "object": "response", "created_at": 1790000000, "status": "completed", "model": model,
		"output": []any{map[string]any{
			"type": "message", "id": "msg_fake", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text(chunks), "annotations": []any{}}},
		}},
		"usage": map[string]any{
			"input_tokens": PromptTokens, "input_tokens_details": map[string]int{"cached_tokens": CachedTokens},
			"output_tokens": chunks, "output_tokens_details": map[string]int{"reasoning_tokens": 0},
			"total_tokens": PromptTokens + chunks,
		},
	}
}

func (s *Server) responsesStream(w http.ResponseWriter, r *http.Request, model string) {
	e := s.stream(w, r)
	created := response(model, 0)
	created["status"], created["output"], created["usage"] = "in_progress", []any{}, nil
	seq := 0
	next := func() int { seq++; return seq }
	if !e.send("response.created", map[string]any{"type": "response.created", "sequence_number": next(), "response": created}) {
		return
	}
	for i := range s.opts.Chunks {
		if !e.send("response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "sequence_number": next(), "item_id": "msg_fake",
			"output_index": 0, "content_index": 0, "delta": fmt.Sprintf("tok%d ", i),
		}) {
			return
		}
	}
	e.send("response.completed", map[string]any{"type": "response.completed", "sequence_number": next(),
		"response": response(model, s.opts.Chunks)})
}

func anthropicUsage(output int) map[string]any {
	return map[string]any{
		"input_tokens": PromptTokens, "cache_creation_input_tokens": CacheWriteTokens,
		"cache_read_input_tokens": CacheReadTokens, "output_tokens": output,
	}
}

func message(model string, chunks int) map[string]any {
	return map[string]any{
		"id": "msg_fake", "type": "message", "role": "assistant", "model": model,
		"content":     []any{map[string]any{"type": "text", "text": text(chunks)}},
		"stop_reason": "end_turn", "stop_sequence": nil, "usage": anthropicUsage(chunks),
	}
}

func (s *Server) messagesStream(w http.ResponseWriter, r *http.Request, model string, pieces []string) {
	e := s.stream(w, r)
	start := message(model, 0)
	start["content"], start["stop_reason"], start["usage"] = []any{}, nil, anthropicUsage(1)
	steps := []struct {
		name string
		data any
	}{
		{"message_start", map[string]any{"type": "message_start", "message": start}},
		{"content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}}},
		{"ping", map[string]any{"type": "ping"}},
	}
	for _, st := range steps {
		if !e.send(st.name, st.data) {
			return
		}
	}
	for _, p := range pieces {
		if !e.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": p}}) {
			return
		}
	}
	steps = []struct {
		name string
		data any
	}{
		{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
		{"message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": s.opts.Chunks}}},
		{"message_stop", map[string]any{"type": "message_stop"}},
	}
	for _, st := range steps {
		if !e.send(st.name, st.data) {
			return
		}
	}
}

// converseCall reads Bedrock's Converse path: /model/{id}/converse or
// /converse-stream.
func converseCall(path string) (id, method string, ok bool) {
	rest, ok := strings.CutPrefix(path, "/model/")
	if !ok {
		return "", "", false
	}
	i := strings.LastIndexByte(rest, '/')
	if i <= 0 {
		return "", "", false
	}
	id, method = rest[:i], rest[i+1:]
	return id, method, method == "converse" || method == "converse-stream"
}

// converse answers as Bedrock's Converse does: a whole answer, or an AWS
// event stream (a model whose id says "throttle-mid-stream" is throttled
// after its first words); a failure as AWS's error. The cache reads and
// writes of Anthropic's answers show when the request has a checkpoint.
func (s *Server) converse(w http.ResponseWriter, r *http.Request, id string, stream bool, body []byte, fail int) {
	awsError := func(status int, kind, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-Errortype", kind+":http://internal.amazon.com/coral/com.amazon.bedrock/")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"message":%q}`, msg)
	}
	switch fail {
	case 0:
	case http.StatusTooManyRequests:
		awsError(fail, "ThrottlingException", "Too many requests, please wait before trying again.")
		return
	default:
		awsError(fail, "InternalServerException", "fake failure")
		return
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
		Model    json.RawMessage   `json:"model"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Messages) == 0 || len(req.Model) > 0 {
		awsError(http.StatusBadRequest, "ValidationException", "Malformed input request, please reformat your input and try again.")
		return
	}
	text := strings.Join(s.pieces(id, body), "")
	if s.opts.Reply != nil && !stream {
		text = s.opts.Reply(id, body)
	}
	usage := map[string]int{"inputTokens": PromptTokens, "outputTokens": s.opts.Chunks, "totalTokens": PromptTokens + s.opts.Chunks}
	if bytes.Contains(body, []byte(`"cachePoint"`)) {
		usage["cacheReadInputTokens"], usage["cacheWriteInputTokens"] = CacheReadTokens, CacheWriteTokens
	}
	w.Header().Set("X-Amzn-Requestid", "0f0e0d0c-aaaa-bbbb-cccc-123456789abc")
	if !stream {
		writeJSON(w, map[string]any{"output": map[string]any{"message": map[string]any{"role": "assistant",
			"content": []any{map[string]string{"text": text}}}}, "stopReason": "end_turn", "usage": usage,
			"metrics": map[string]int{"latencyMs": 1}})
		return
	}
	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	enc := eventstream.NewEncoder()
	send := func(kind, name string, payload any) {
		var h eventstream.Headers
		h.Set(":message-type", eventstream.StringValue(kind))
		if kind == "exception" {
			h.Set(":exception-type", eventstream.StringValue(name))
		} else {
			h.Set(":event-type", eventstream.StringValue(name))
		}
		h.Set(":content-type", eventstream.StringValue("application/json"))
		b, _ := json.Marshal(payload)
		_ = enc.Encode(w, eventstream.Message{Headers: h, Payload: b})
		w.(http.Flusher).Flush()
	}
	send("event", "messageStart", map[string]string{"role": "assistant", "p": "abcdefghijklmnop"})
	for i, piece := range s.pieces(id, body) {
		if !s.sleep(r, s.opts.ChunkDelay) {
			return
		}
		send("event", "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]string{"text": piece}})
		if i == 0 && strings.Contains(id, "throttle-mid-stream") {
			send("exception", "throttlingException", map[string]string{"message": "Too many requests, please wait before trying again."})
			return
		}
	}
	send("event", "contentBlockStop", map[string]int{"contentBlockIndex": 0})
	send("event", "messageStop", map[string]string{"stopReason": "end_turn"})
	send("event", "metadata", map[string]any{"usage": usage, "metrics": map[string]int{"latencyMs": 1}})
}
