// Package fakeupstream is a stand-in LLM provider for tests and benchmarks.
// It speaks the OpenAI Chat Completions, Responses and Embeddings APIs and
// the Anthropic Messages API, streamed or not, answers after set delays and
// reports fixed token counts, so what the gateway adds can be measured and
// what it reads can be checked.
package fakeupstream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
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
}

// Request is a call the server received.
type Request struct {
	Path   string
	Header http.Header
	Body   []byte
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
// with or without /v1: /v1/chat/completions and /chat/completions alike.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	s.mu.Lock()
	s.requests = append(s.requests, Request{Path: path, Header: r.Header.Clone(), Body: body})
	fail := 0
	if len(s.failures) > 0 {
		fail, s.failures = s.failures[0], s.failures[1:]
	}
	s.mu.Unlock()

	if !s.sleep(r, s.opts.TTFB) {
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
	if json.Unmarshal(body, &req) != nil || req.Model == "" {
		http.Error(w, `{"error":{"message":"bad request","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}
	switch path {
	case "/chat/completions":
		if req.Stream {
			s.chatStream(w, r, req.Model, req.StreamOptions != nil && req.StreamOptions.IncludeUsage)
		} else {
			writeJSON(w, chatCompletion(req.Model, s.opts.Chunks))
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
			s.messagesStream(w, r, req.Model)
		} else {
			writeJSON(w, message(req.Model, s.opts.Chunks))
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

func (s *Server) chatStream(w http.ResponseWriter, r *http.Request, model string, usage bool) {
	e := s.stream(w, r)
	chunk := func(delta map[string]any, finish any) map[string]any {
		c := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 1790000000, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish, "logprobs": nil}},
		}
		if usage {
			c["usage"] = nil
		}
		return c
	}
	if !e.send("", chunk(map[string]any{"role": "assistant", "content": ""}, nil)) {
		return
	}
	for i := range s.opts.Chunks {
		if !e.send("", chunk(map[string]any{"content": fmt.Sprintf("tok%d ", i)}, nil)) {
			return
		}
	}
	if !e.send("", chunk(map[string]any{}, "stop")) {
		return
	}
	if usage {
		last := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 1790000000, "model": model,
			"choices": []any{}, "usage": openAIUsage(s.opts.Chunks),
		}
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

func (s *Server) messagesStream(w http.ResponseWriter, r *http.Request, model string) {
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
	for i := range s.opts.Chunks {
		if !e.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": fmt.Sprintf("tok%d ", i)}}) {
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
