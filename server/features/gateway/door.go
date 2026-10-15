package gateway

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/converse"
	"github.com/hasan-muzak/promptdam/server/features/gemini"
	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// Doors serve providers' own APIs to clients: Google's Gemini API at
// /gemini/ (google-genai, Vercel's and LangChain's Google clients) and
// Bedrock's runtime API at /bedrock/ (AWS's SDKs, AnthropicBedrock). A
// door converts the request at the edge into an API the gateway speaks
// (Gemini's into Chat Completions, Bedrock's into Messages), so the call
// is routed, cached, filtered and metered as any other, and converts all
// the client gets back: answers, streams and every error.

const (
	geminiPrefix  = "/gemini/"
	bedrockPrefix = "/bedrock/"
)

// IsDoor reports a path a door serves. Their SDKs may send paths a
// ServeMux would redirect (a doubled slash, an escaped model id): they are
// to reach the Handler as they are.
func IsDoor(path string) bool {
	return strings.HasPrefix(path, geminiPrefix) || strings.HasPrefix(path, bedrockPrefix)
}

// Doors, as the request log names them.
const (
	DoorGemini   = "gemini"
	DoorConverse = "bedrock-converse"
	DoorInvoke   = "bedrock-invoke"
)

// door is a call through a door.
type door struct {
	name    string
	gemini  gemini.Op
	bedrock converse.Op
	// sse is Gemini's stream as server-sent events (alt=sse), not the JSON
	// array it streams otherwise.
	sse bool
	// dropped are the request's fields the door left out; fillMax marks a
	// Messages call without max_tokens, sent with the target model's own.
	dropped []string
	fillMax bool
	// betas are the Anthropic betas an InvokeModel body asked for.
	betas []string
	start time.Time
	bytes int // the request's size, as the client sent it
}

// aws reports Bedrock's door (its path known or not).
func (d *door) aws() bool { return d.name != DoorGemini }

// serveDoor serves a call through a door.
func (h *Handler) serveDoor(w http.ResponseWriter, r *http.Request) {
	d := &door{start: time.Now()}
	trace := h.cfg.NewTrace()
	w.Header().Set(brand.HeaderTrace, trace)
	var model string
	if rest, ok := strings.CutPrefix(r.URL.Path, geminiPrefix); ok {
		d.name = DoorGemini
		call, ok := gemini.ParsePath(rest)
		if !ok {
			doorError(w, d, http.StatusNotFound, "unknown_endpoint", r.Method+" "+r.URL.Path+" is not a path of Gemini's API Promptdam serves")
			return
		}
		d.gemini, model = call.Op, call.Model
		d.sse = r.URL.Query().Get("alt") == "sse"
		// Google's SDKs send the key as x-goog-api-key, its REST examples
		// as ?key=.
		if r.Header.Get("X-Api-Key") == "" && r.Header.Get("Authorization") == "" {
			key := r.Header.Get("X-Goog-Api-Key")
			if key == "" {
				key = r.URL.Query().Get("key")
			}
			if key != "" {
				r.Header.Set("X-Api-Key", key)
			}
		}
		if call.Op == gemini.OpList || call.Op == gemini.OpGet {
			h.geminiModels(w, r, d, trace, call)
			return
		}
	} else {
		d.name = DoorConverse
		var ok bool
		model, d.bedrock, ok = converse.ParsePath(strings.TrimPrefix(r.URL.EscapedPath(), strings.TrimSuffix(bedrockPrefix, "/")))
		if !ok {
			doorError(w, d, http.StatusNotFound, "unknown_endpoint", r.Method+" "+r.URL.Path+" is not a path of Bedrock's runtime API Promptdam serves")
			return
		}
		if d.bedrock == converse.OpInvoke || d.bedrock == converse.OpInvokeStream {
			d.name = DoorInvoke
		}
		if strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-") {
			doorError(w, d, http.StatusForbidden, "aws_signature",
				"Promptdam takes your Promptdam key as a Bedrock API key (Authorization: Bearer), not AWS credentials: "+
					"set the client's bearer token to your Promptdam key")
			return
		}
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		doorError(w, d, http.StatusMethodNotAllowed, "method_not_allowed", r.URL.Path+" accepts POST only")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBody))
	if err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			doorError(w, d, http.StatusRequestEntityTooLarge, "request_too_large",
				"the request body is larger than "+strconv.FormatInt(h.cfg.MaxBody>>20, 10)+" MiB")
			return
		}
		doorError(w, d, http.StatusBadRequest, "body_unreadable", "the request body could not be read")
		return
	}
	d.bytes = len(body)
	path, out, err := d.convert(body, model)
	if err != nil {
		msg := err.Error()
		var te *translate.Error
		if errors.As(err, &te) {
			msg = te.Message
		}
		doorError(w, d, http.StatusBadRequest, "untranslatable_request", "Promptdam can't serve this request: "+msg)
		return
	}
	// The client's own headers for its API (AWS's event-stream Accept)
	// mustn't reach a provider.
	r.Header.Del("Accept")
	r.Header.Del("X-Amzn-Bedrock-Accept")
	r.Header.Set("Content-Type", "application/json")
	if len(d.betas) > 0 {
		betas := d.betas
		if v := r.Header.Get("Anthropic-Beta"); v != "" {
			betas = append(strings.Split(v, ","), betas...)
		}
		r.Header.Set("Anthropic-Beta", strings.Join(betas, ","))
	}
	r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(out)), int64(len(out))
	ep := endpoints[path]
	dw := &doorWriter{w: w, d: d}
	c := &call{h: h, w: dw, r: r, ep: ep, door: d, rec: Record{Trace: trace, Format: ep.format, Start: d.start, Door: d.name}}
	h.run(c)
}

// convert turns the client's request into one the gateway speaks, and
// names the gateway path that serves it.
func (d *door) convert(body []byte, model string) (path string, out []byte, err error) {
	switch d.gemini {
	case gemini.OpGenerate, gemini.OpStream:
		out, d.dropped, err = gemini.Request(body, model, d.gemini == gemini.OpStream)
		return "/v1/chat/completions", out, err
	case gemini.OpEmbed, gemini.OpBatchEmbed:
		out, d.dropped, err = gemini.EmbedRequest(body, model, d.gemini == gemini.OpBatchEmbed)
		return "/v1/embeddings", out, err
	case gemini.OpCount:
		return "", nil, errors.New("countTokens isn't served yet")
	}
	var c converse.Converted
	switch d.bedrock {
	case converse.OpConverse, converse.OpConverseStream:
		c, err = converse.FromConverse(body, model, d.bedrock.Stream())
	case converse.OpInvoke, converse.OpInvokeStream:
		c, err = converse.FromInvoke(body, model, d.bedrock.Stream())
	default:
		return "", nil, errors.New("count-tokens isn't served yet")
	}
	d.dropped, d.fillMax, d.betas = c.Dropped, c.NoMaxTokens, c.Betas
	return "/v1/messages", c.Body, err
}

// fit makes a door's call right for the target t (body is in t's format
// f): Messages wants a limit Converse needn't name, and OpenAI's own API
// takes max_completion_tokens.
func (d *door) fit(body []byte, f wire.Format, t Target) []byte {
	switch {
	case f == wire.AnthropicMessages && d.fillMax:
		if _, ok, _ := wire.Field(body, "max_tokens"); !ok {
			n := t.MaxTokens
			if n <= 0 {
				n = translate.DefaultMaxTokens
			}
			if out, err := wire.SetField(body, "max_tokens", []byte(strconv.Itoa(n))); err == nil {
				return out
			}
		}
	case f == wire.OpenAIChat && d.gemini != 0 && translate.MaxCompletionTokens(t.Kind):
		if v, ok, _ := wire.Field(body, "max_tokens"); ok {
			if out, err := wire.DeleteField(body, "max_tokens"); err == nil {
				if out, err = wire.SetField(out, "max_completion_tokens", v); err == nil {
					return out
				}
			}
		}
	}
	return body
}

// geminiModels answers Gemini's model list (and one model's entry).
func (h *Handler) geminiModels(w http.ResponseWriter, r *http.Request, d *door, trace string, call gemini.Call) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		doorError(w, d, http.StatusMethodNotAllowed, "method_not_allowed", r.URL.Path+" accepts GET only")
		return
	}
	lister, ok := h.cfg.Resolver.(ModelLister)
	if !ok {
		doorError(w, d, http.StatusNotFound, "unknown_endpoint", "the model list is not available")
		return
	}
	models, err := lister.Models(r.Context(), &Call{Format: wire.OpenAIChat, Header: r.Header, Trace: trace, Client: h.clientKey(r)})
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = &Error{Status: http.StatusInternalServerError, Code: "internal_error", Message: "the gateway failed"}
		}
		doorError(w, d, e.Status, e.Code, e.Message)
		return
	}
	seen := make(map[string]bool, len(models))
	ids := make([]string, 0, len(models))
	for _, m := range models {
		if !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}
	out := gemini.Models(ids)
	if call.Op == gemini.OpGet {
		if !seen[call.Model] {
			doorError(w, d, http.StatusNotFound, "unknown_model", "no provider is set up for model "+call.Model)
			return
		}
		out = gemini.Model(call.Model)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// doorError answers with the gateway's own error, in the door's shape.
func doorError(w http.ResponseWriter, d *door, status int, code, msg string) {
	h := w.Header()
	h.Set(brand.HeaderErrorCode, code)
	h.Set("Content-Type", "application/json")
	var out []byte
	if d.aws() {
		var kind string
		status, kind, out = converse.ClientError(status, []byte(`{"message":`+strconv.Quote(msg)+`}`))
		h.Set("X-Amzn-Errortype", kind)
		h.Set("X-Amzn-Requestid", h.Get(brand.HeaderTrace))
	} else {
		out = gemini.Error(gemini.Status(status), msg)
		status = gemini.Status(status)
	}
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

// doorWriter converts what the gateway writes, in the API it served the
// call in, into the door's: a stream event by event as it comes, a whole
// answer or an error once it is all written (finish).
type doorWriter struct {
	w      http.ResponseWriter
	d      *door
	status int
	// stream is a successful answer streamed: each event converted by
	// event, the stream's end by end.
	stream bool
	event  func(out []byte, name string, data []byte) []byte
	end    func(out []byte) []byte
	buf    []byte // a whole answer, or the stream's incomplete event
	out    []byte
}

func (dw *doorWriter) Header() http.Header { return dw.w.Header() }

func (dw *doorWriter) WriteHeader(status int) {
	if dw.status != 0 {
		return
	}
	dw.status = status
	h := dw.w.Header()
	h.Del("Content-Length")
	if len(dw.d.dropped) > 0 {
		dropped := dw.d.dropped
		if v := h.Get(brand.HeaderDropped); v != "" {
			dropped = append(strings.Split(v, ","), dropped...)
		}
		h.Set(brand.HeaderDropped, strings.Join(dropped, ","))
	}
	if status >= 300 || !eventStream(h) {
		return // converted whole, at finish
	}
	dw.stream = true
	switch {
	case dw.d.gemini != 0:
		s := gemini.NewStream(dw.d.sse)
		dw.event = func(out []byte, _ string, data []byte) []byte { return s.Event(out, data) }
		dw.end = s.End
		h.Set("Content-Type", s.ContentType())
	default:
		s := converse.NewEventStream(dw.d.bedrock == converse.OpInvokeStream, dw.d.start)
		dw.event, dw.end = s.Event, s.End
		h.Set("Content-Type", converse.ContentType)
		h.Set("X-Amzn-Requestid", h.Get(brand.HeaderTrace))
		if dw.d.bedrock == converse.OpInvokeStream {
			h.Set("X-Amzn-Bedrock-Content-Type", "application/json")
		}
	}
	dw.w.WriteHeader(status)
}

func (dw *doorWriter) Write(p []byte) (int, error) {
	if dw.status == 0 {
		dw.WriteHeader(http.StatusOK)
	}
	dw.buf = append(dw.buf, p...)
	if !dw.stream {
		return len(p), nil
	}
	dw.out = dw.out[:0]
	rest := dw.buf
	for {
		end, sep := eventEnd(rest)
		if end < 0 {
			break
		}
		name, data := eventFields(rest[:end])
		dw.out = dw.event(dw.out, name, data)
		rest = rest[end+sep:]
	}
	dw.buf = dw.buf[:copy(dw.buf, rest)]
	if len(dw.out) > 0 {
		if _, err := dw.w.Write(dw.out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush sends what is converted so far.
func (dw *doorWriter) Flush() {
	if dw.stream {
		_ = http.NewResponseController(dw.w).Flush()
	}
}

// finish writes what the door holds: a whole answer or an error, converted,
// or the stream's end. A call cut off (aborted) gets nothing more.
func (dw *doorWriter) finish(aborted bool) {
	if aborted {
		return
	}
	if dw.stream {
		if out := dw.end(nil); len(out) > 0 {
			_, _ = dw.w.Write(out)
		}
		return
	}
	status := dw.status
	if status == 0 {
		return // nothing was written
	}
	h := dw.w.Header()
	h.Set("Content-Type", "application/json")
	out, err := dw.whole(status, h)
	if err != nil {
		h.Set(brand.HeaderErrorCode, "unconvertible_answer")
		out = dw.errorBody(http.StatusBadGateway, h, []byte(`{"error":{"message":"the provider's answer couldn't be converted"}}`))
	}
	dw.w.WriteHeader(dw.status)
	_, _ = dw.w.Write(out)
}

// whole converts a whole answer (or an error, status ≥ 300) into the
// door's, setting its headers in h.
func (dw *doorWriter) whole(status int, h http.Header) ([]byte, error) {
	if status >= 300 {
		return dw.errorBody(status, h, dw.buf), nil
	}
	d := dw.d
	switch d.gemini {
	case gemini.OpGenerate, gemini.OpStream:
		return gemini.Response(dw.buf)
	case gemini.OpEmbed, gemini.OpBatchEmbed:
		return gemini.EmbedResponse(dw.buf, d.gemini == gemini.OpBatchEmbed)
	}
	h.Set("X-Amzn-Requestid", h.Get(brand.HeaderTrace))
	switch d.bedrock {
	case converse.OpConverse, converse.OpConverseStream:
		return converse.ToConverse(dw.buf, time.Since(d.start))
	}
	if in, out, ok := converse.TokenCounts(dw.buf); ok {
		h.Set("X-Amzn-Bedrock-Input-Token-Count", strconv.FormatInt(in, 10))
		h.Set("X-Amzn-Bedrock-Output-Token-Count", strconv.FormatInt(out, 10))
	}
	return dw.buf, nil // InvokeModel answers Claude's Messages as they are
}

// errorBody converts an error answer, setting the status the door answers
// with.
func (dw *doorWriter) errorBody(status int, h http.Header, body []byte) []byte {
	if dw.d.aws() {
		var kind string
		var out []byte
		dw.status, kind, out = converse.ClientError(status, body)
		h.Set("X-Amzn-Errortype", kind)
		h.Set("X-Amzn-Requestid", h.Get(brand.HeaderTrace))
		return out
	}
	dw.status = gemini.Status(status)
	return gemini.ErrorBody(status, body)
}

// eventEnd finds the blank line ending the first server-sent event in b:
// its index and length, -1 when the event isn't complete.
func eventEnd(b []byte) (int, int) {
	lf := bytes.Index(b, []byte("\n\n"))
	crlf := bytes.Index(b, []byte("\r\n\r\n"))
	switch {
	case crlf >= 0 && (lf < 0 || crlf < lf):
		return crlf, 4
	case lf >= 0:
		return lf, 2
	}
	return -1, 0
}

// eventFields are an event's name and data (its data lines joined).
func eventFields(ev []byte) (name string, data []byte) {
	for line := range bytes.SplitSeq(ev, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			name = string(bytes.TrimSpace(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			v := bytes.TrimPrefix(line[len("data:"):], []byte(" "))
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, v...)
		}
	}
	return name, data
}
