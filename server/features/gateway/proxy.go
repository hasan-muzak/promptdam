package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/promptcache"
	"github.com/hasan-muzak/promptdam/server/features/responsecache"
	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// statusClientClosed is logged for calls the client gave up on (nginx's
// convention); the client never sees it.
const statusClientClosed = 499

// maxMeteredBody bounds the non-streamed response bodies kept to read their
// usage; larger ones are forwarded unread.
const maxMeteredBody = 16 << 20

var bodies = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// call is one request in flight.
type call struct {
	h   *Handler
	w   http.ResponseWriter
	r   *http.Request
	ep  endpoint
	rec Record
	// cacheKey and cacheTTL store a successful answer for a route that
	// caches them.
	cacheKey string
	cacheTTL time.Duration
	// promptCache adds Anthropic cache breakpoints, to calls translated to
	// the Messages API too.
	promptCache bool
	// sample keeps the answer the client gets in answer (for a router's
	// evaluation) until it grows past MaxSampledAnswer.
	sample, overflow bool
	answer           []byte
	// filter rewrites the provider's answer (Route.Response).
	filter ResponseFilter
}

// keep adds bytes the client got to a sampled call's answer.
func (c *call) keep(p []byte) {
	if !c.sample || c.overflow {
		return
	}
	if len(c.answer)+len(p) > MaxSampledAnswer {
		c.overflow, c.answer = true, nil
		return
	}
	c.answer = append(c.answer, p...)
}

func (c *call) serve() {
	buf := bodies.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 4<<20 {
			bodies.Put(buf)
		}
	}()
	if _, err := buf.ReadFrom(http.MaxBytesReader(c.w, c.r.Body, c.h.cfg.MaxBody)); err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			c.fail(&Error{Status: http.StatusRequestEntityTooLarge, Type: TypeTooLarge, Code: "request_too_large",
				Message: "the request body is larger than " + strconv.FormatInt(c.h.cfg.MaxBody>>20, 10) + " MiB"})
			return
		}
		c.fail(&Error{Status: http.StatusBadRequest, Type: TypeInvalidRequest, Code: "body_unreadable", Message: "the request body could not be read"})
		return
	}
	body := buf.Bytes()
	c.rec.RequestBytes = len(body)
	req, err := wire.ParseRequest(c.ep.format, body)
	if err != nil {
		msg := "the request body is not valid JSON"
		if errors.Is(err, wire.ErrNoModel) {
			msg = "the request names no model"
		}
		c.fail(&Error{Status: http.StatusBadRequest, Type: TypeInvalidRequest, Code: "invalid_request", Message: msg})
		return
	}
	c.rec.RequestedModel, c.rec.Stream, c.rec.Request = req.Model, req.Stream, req
	c.rec.Shape = families.Of(req.Prefix, c.r.Header.Get(brand.HeaderFamily))

	route, err := c.h.cfg.Resolver.Resolve(c.r.Context(), &Call{
		Format: c.ep.format, Request: req, Header: c.r.Header, Trace: c.rec.Trace, Client: c.h.clientKey(c.r),
		Shape: c.rec.Shape, BodyBytes: len(body), Body: body,
	})
	if err != nil {
		if e, ok := errors.AsType[*Error](err); ok && e.Meta != nil {
			c.rec.Meta = e.Meta
		}
		c.fail(err)
		return
	}
	c.rec.Meta = route.Meta
	for k, v := range route.Header {
		c.w.Header()[k] = v
	}
	if route.Body != nil {
		if masked, err := wire.ParseRequest(c.ep.format, route.Body); err == nil {
			body, req = route.Body, masked
		}
	}
	c.filter = route.Response
	if c.filter != nil {
		defer c.filter.End()
	}
	if route.Sample {
		// The request as the client sent it; buf goes back to its pool.
		c.sample, c.rec.Body = true, bytes.Clone(body)
	}
	if len(route.Targets) == 0 {
		c.fail(&Error{Status: http.StatusNotFound, Type: TypeNotFound, Code: "no_provider",
			Message: "no provider is set up for model " + req.Model})
		return
	}

	// Routes with the response cache answer identical non-streamed calls
	// from it, once the caller passed every check.
	if route.CacheTTL > 0 && c.h.cfg.Cache != nil && !req.Stream && c.ep.format != wire.AnthropicCountTokens {
		if key, ok := responsecache.Key(route.CacheScope, c.ep.format, body); ok {
			if e, hit := c.h.cfg.Cache.Get(c.r.Context(), key); hit {
				c.serveCached(e)
				return
			}
			c.cacheKey, c.cacheTTL, c.rec.Cache = key, route.CacheTTL, "miss"
		}
	}

	c.promptCache = route.PromptCache
	if route.PromptCache && c.ep.format == wire.AnthropicMessages {
		body, c.rec.PromptCached = promptcache.Mark(body, req)
	}

	// Token counts come in streams only when asked for: ask on the
	// client's behalf, and leave the extra chunk out of what it receives.
	dropUsage := false
	if c.ep.format == wire.OpenAIChat && req.Stream && !req.IncludeUsage {
		if withUsage, err := wire.WithStreamUsage(body); err == nil {
			body, dropUsage = withUsage, true
		}
	}

	// Each target is tried, and retried after a jittered pause, until one
	// answers; a target whose circuit is open is skipped, but for the last.
	for i, t := range route.Targets {
		last := i == len(route.Targets)-1
		if !last && !c.h.breakers.allow(t.ID, time.Now()) {
			continue
		}
		for try := 0; try <= max(route.Retries, 0); try++ {
			if try > 0 && !pause(c.r.Context(), backoff(try)) {
				c.rec.Status, c.rec.ErrorCode = statusClientClosed, "client_closed"
				return
			}
			c.rec.Attempts++
			c.rec.Fallback = i > 0
			// A call no provider answered is logged with the one it tried
			// last: provider outages show in the log.
			c.rec.Provider, c.rec.Kind, c.rec.TargetModel, c.rec.ServedModel = t.Provider, t.Kind, t.Model, t.Model
			done, failed := c.attempt(t, req, body, dropUsage, last && try == max(route.Retries, 0))
			c.h.breakers.record(t.ID, !failed, time.Now())
			if done {
				return
			}
		}
	}
}

// backoff is the pause before retry n (1, 2, …): full jitter up to
// 250 ms × 2ⁿ⁻¹, at most 2 s.
func backoff(n int) time.Duration {
	ceiling := min(250*time.Millisecond<<(n-1), 2*time.Second)
	return rand.N(ceiling)
}

// pause waits d; false when the client left first.
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// attempt sends the call to one target and relays the answer. done is
// false when the target failed before anything reached the client and the
// call may go on (final is false); failed reports a failure of the provider
// (unreachable, rate limiting, overloaded, a server error), for its circuit.
func (c *call) attempt(t Target, req wire.Request, body []byte, dropUsage, final bool) (done, failed bool) {
	format, path := t.formatOf(c.ep.format), c.ep.path
	var dropped []string
	switch {
	case format != c.ep.format:
		// A target of the other API format gets the call translated; one
		// that can't express it is passed over for the next.
		out, d, err := translate.Request(c.ep.format, format, body,
			translate.Options{Model: t.Model, Kind: t.Kind, Reasoning: t.Reasoning, MaxTokens: t.MaxTokens})
		if err != nil {
			if !final {
				return false, false
			}
			msg := "the request can't be sent to " + t.Model
			var te *translate.Error
			if errors.As(err, &te) {
				msg += ": " + te.Message
			}
			c.fail(&Error{Status: http.StatusBadRequest, Type: TypeInvalidRequest, Code: "untranslatable_request", Message: msg})
			return true, false
		}
		body, dropped, path, dropUsage = out, d, paths[format], false
		if c.promptCache && format == wire.AnthropicMessages {
			if areq, err := wire.ParseRequest(format, body); err == nil {
				body, c.rec.PromptCached = promptcache.Mark(body, areq)
			}
		}
	case t.Model != req.Model:
		model, _ := json.Marshal(t.Model)
		swapped, err := wire.SetField(body, "model", model)
		if err != nil {
			c.fail(&Error{Status: http.StatusBadRequest, Type: TypeInvalidRequest, Code: "invalid_request", Message: "the request body is not valid JSON"})
			return true, false
		}
		body = swapped
	}
	ctx, cancel := context.WithCancel(c.r.Context())
	defer cancel()
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_misconfigured", Message: "the provider's base URL is invalid"})
		return true, false
	}
	upstreamHeader(up.Header, c.r.Header, format, t.Header)

	sent := time.Now()
	res, err := c.h.cfg.Transport.RoundTrip(up)
	c.rec.Upstream += time.Since(sent)
	if err != nil {
		if c.r.Context().Err() != nil {
			c.rec.Status, c.rec.ErrorCode = statusClientClosed, "client_closed"
			return true, false
		}
		if !final {
			return false, true
		}
		e := &Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_unreachable", Message: "the provider could not be reached"}
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			e.Status, e.Code, e.Message = http.StatusGatewayTimeout, "provider_timeout", "the provider did not answer in time"
		}
		c.fail(e)
		return true, true
	}
	if retryable(res.StatusCode) && !final {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		_ = res.Body.Close()
		return false, true
	}
	defer func() { _ = res.Body.Close() }()
	c.relay(res, t, cancel, dropUsage, format, dropped)
	return true, retryable(res.StatusCode)
}

// relay streams a provider's response to the client, reading its usage.
// format is the API the provider answers in: a call translated to it gets
// its answer translated back.
func (c *call) relay(res *http.Response, t Target, cancel context.CancelFunc, dropUsage bool, format wire.Format, dropped []string) {
	c.rec.Provider, c.rec.Kind, c.rec.ServedModel, c.rec.Status = t.Provider, t.Kind, t.Model, res.StatusCode
	c.rec.TargetModel = t.Model
	translating := format != c.ep.format
	c.rec.Translated, c.rec.Dropped = translating, dropped
	h := c.w.Header()
	responseHeader(h, res.Header)
	h.Set(brand.HeaderTrace, c.rec.Trace)
	h.Set(brand.HeaderProvider, t.Provider)
	h.Set(brand.HeaderServedModel, t.Model)
	if translating || c.filter != nil {
		h.Del("Content-Length") // the answer is rewritten
	}
	if len(dropped) > 0 {
		h.Set(brand.HeaderDropped, strings.Join(dropped, ","))
	}
	switch {
	case c.rec.Cache != "":
		h.Set(brand.HeaderCache, c.rec.Cache)
	case c.rec.PromptCached:
		h.Set(brand.HeaderCache, "prompt")
	}

	// An upstream that goes quiet for longer than the idle timeout is cut.
	body := &idleReader{r: res.Body, d: c.h.cfg.IdleTimeout}
	body.timer = time.AfterFunc(body.d, func() { body.fired.Store(true); cancel() })
	defer body.timer.Stop()

	meter := wire.NewMeter(format)
	rc := http.NewResponseController(c.w)
	var readErr error
	switch {
	case eventStream(res.Header):
		h.Del("Content-Length")
		c.w.WriteHeader(res.StatusCode)
		_ = rc.Flush()
		var st translate.Stream
		if translating {
			st, _ = translate.NewStream(format, c.ep.format, c.rec.Request.IncludeUsage)
		}
		var out []byte
		gone := false
		s := wire.NewScanner(body, 0)
		for s.Next() {
			ev := s.Event()
			usage := meter.Event(ev)
			ev = c.filterEvent(ev)
			if st != nil {
				if out = st.Event(out[:0], ev); len(out) > 0 {
					if gone = !c.write(out); gone {
						break
					}
					_ = rc.Flush()
				}
				continue
			}
			if usage && dropUsage {
				continue
			}
			if gone = !c.write(ev.Raw); gone {
				break
			}
			_ = rc.Flush()
		}
		readErr = s.Err()
		if st != nil && !gone && readErr == nil && !body.fired.Load() {
			if out = st.End(out[:0]); len(out) > 0 {
				c.write(out)
				_ = rc.Flush()
			}
		}
	case translating:
		readErr = c.relayTranslated(res, t, body, meter, format)
	case c.filter != nil:
		readErr = c.relayFiltered(res, t, body, meter)
	default:
		c.w.WriteHeader(res.StatusCode)
		keep := res.StatusCode < 300
		var kept []byte
		buf := make([]byte, 32<<10)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				if keep {
					if len(kept)+n <= maxMeteredBody {
						kept = append(kept, buf[:n]...)
					} else {
						keep, kept = false, nil
					}
				}
				if !c.write(buf[:n]) {
					break
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr = err
				}
				break
			}
		}
		if keep {
			meter.Body(kept)
			if c.cacheKey != "" && res.StatusCode == http.StatusOK && readErr == nil {
				c.store(res.StatusCode, res.Header.Get("Content-Type"), t, meter, kept)
			}
		}
	}

	c.rec.Usage, c.rec.UsageKnown = meter.Usage()
	if m := meter.Model(); m != "" {
		c.rec.ServedModel = m
	}
	if c.rec.ErrorCode != "" {
		return // the gateway answered with its own error
	}
	if c.sample && !c.overflow && res.StatusCode == http.StatusOK && readErr == nil {
		c.rec.Answer = c.answer
	}
	switch {
	case c.r.Context().Err() != nil:
		c.rec.Status, c.rec.ErrorCode = statusClientClosed, "client_closed"
	case body.fired.Load():
		c.rec.ErrorCode = "provider_idle_timeout"
	case readErr != nil:
		c.rec.ErrorCode = "provider_stream_broken"
	case meter.StreamError() != "":
		c.rec.ErrorCode = "provider_" + meter.StreamError()
	case res.StatusCode >= 400:
		c.rec.ErrorCode = "provider_" + strconv.Itoa(res.StatusCode)
	}
}

// relayTranslated answers a call translated to another API format with the
// provider's whole answer, or its error, translated back.
func (c *call) relayTranslated(res *http.Response, t Target, body io.Reader, meter *wire.Meter, format wire.Format) error {
	kept, err := io.ReadAll(io.LimitReader(body, maxMeteredBody+1))
	if err != nil {
		c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_stream_broken", Message: "the provider's answer broke off"})
		return err
	}
	if len(kept) > maxMeteredBody {
		c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "answer_too_large",
			Message: "the provider's answer is too large to translate"})
		return nil
	}
	var out []byte
	if res.StatusCode < 300 {
		meter.Body(kept)
		if out, err = translate.Response(format, c.ep.format, kept); err != nil {
			c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "untranslatable_answer",
				Message: "the provider's answer couldn't be translated"})
			return nil
		}
		if c.cacheKey != "" && res.StatusCode == http.StatusOK {
			c.store(res.StatusCode, "application/json", t, meter, out)
		}
	} else {
		out = translate.ErrorBody(c.ep.format, res.StatusCode, kept)
	}
	out = c.filterBody(out)
	c.w.Header().Set("Content-Type", "application/json")
	c.w.WriteHeader(res.StatusCode)
	c.write(out)
	return nil
}

// relayFiltered answers with the provider's whole answer, through the
// route's response filter. An answer too large to hold is relayed as it
// comes, unfiltered and unmetered: what it can hold back is placeholders,
// never the values behind them.
func (c *call) relayFiltered(res *http.Response, t Target, body io.Reader, meter *wire.Meter) error {
	kept, err := io.ReadAll(io.LimitReader(body, maxMeteredBody+1))
	if err != nil {
		c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_stream_broken", Message: "the provider's answer broke off"})
		return err
	}
	if len(kept) > maxMeteredBody {
		c.w.WriteHeader(res.StatusCode)
		if !c.write(kept) {
			return nil
		}
		buf := make([]byte, 32<<10)
		for {
			n, err := body.Read(buf)
			if n > 0 && !c.write(buf[:n]) {
				return nil
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	}
	if res.StatusCode < 300 {
		meter.Body(kept)
		if c.cacheKey != "" && res.StatusCode == http.StatusOK {
			c.store(res.StatusCode, res.Header.Get("Content-Type"), t, meter, kept)
		}
	}
	c.w.WriteHeader(res.StatusCode)
	c.write(c.filterBody(kept))
	return nil
}

// filterBody passes a whole answer through the route's response filter.
func (c *call) filterBody(doc []byte) []byte {
	if c.filter != nil {
		if out := c.filter.Body(doc); out != nil {
			return out
		}
	}
	return doc
}

// filterEvent passes a provider's event through the route's response
// filter; a partial event (longer than the scanner holds) goes as it is.
func (c *call) filterEvent(ev wire.Event) wire.Event {
	if c.filter == nil || ev.Partial || len(ev.Data) == 0 {
		return ev
	}
	data := c.filter.Event(ev.Data)
	if data == nil {
		return ev
	}
	return wire.Event{Name: ev.Name, Data: data, Raw: frame(ev.Name, data)}
}

// frame writes an event as a stream carries it.
func frame(name string, data []byte) []byte {
	b := make([]byte, 0, len(name)+len(data)+16)
	if name != "" {
		b = append(append(append(b, "event: "...), name...), '\n')
	}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		b = append(append(append(b, "data: "...), line...), '\n')
	}
	return append(b, '\n')
}

// serveCached answers from the response cache.
func (c *call) serveCached(e *responsecache.Entry) {
	h := c.w.Header()
	h.Set("Content-Type", e.ContentType)
	h.Set(brand.HeaderTrace, c.rec.Trace)
	h.Set(brand.HeaderProvider, e.Provider)
	h.Set(brand.HeaderServedModel, e.Model)
	h.Set(brand.HeaderCache, "hit")
	c.w.WriteHeader(e.Status)
	c.write(c.filterBody(e.Body))
	c.rec.Status, c.rec.Cache = e.Status, "hit"
	c.rec.Provider, c.rec.Kind, c.rec.ServedModel, c.rec.TargetModel = e.Provider, e.Kind, e.Model, c.rec.RequestedModel
	c.rec.Usage, c.rec.UsageKnown = e.Usage, true
}

// store caches a successful answer, in the background.
func (c *call) store(status int, contentType string, t Target, meter *wire.Meter, body []byte) {
	if len(body) > responsecache.MaxEntry {
		return
	}
	usage, _ := meter.Usage()
	model := meter.Model()
	if model == "" {
		model = t.Model
	}
	e := &responsecache.Entry{Status: status, ContentType: contentType, Body: bytes.Clone(body),
		Provider: t.Provider, Kind: t.Kind, Model: model, Usage: usage}
	key, ttl := c.cacheKey, c.cacheTTL
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.h.cfg.Cache.Put(ctx, key, e, ttl)
	}()
}

// write sends bytes to the client; false when the client is gone.
func (c *call) write(p []byte) bool {
	if c.rec.ResponseBytes == 0 && len(p) > 0 {
		c.rec.TTFB = time.Since(c.rec.Start)
	}
	n, err := c.w.Write(p)
	c.rec.ResponseBytes += int64(n)
	c.keep(p[:n])
	return err == nil
}

// fail answers with the gateway's own error.
func (c *call) fail(err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Status: http.StatusInternalServerError, Type: TypeAPI, Code: "internal_error", Message: "the gateway failed"}
	}
	c.rec.Status, c.rec.ErrorCode = e.Status, e.Code
	c.w.Header().Set(brand.HeaderTrace, c.rec.Trace)
	writeError(c.w, c.ep.format, e)
	c.rec.TTFB = time.Since(c.rec.Start)
}

// retryable statuses send a call to the next target: rate limits, overload
// (Anthropic's 529) and server errors.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == 529 || status >= 500
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

func eventStream(h http.Header) bool {
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && mt == "text/event-stream"
}

// idleReader cancels the upstream call when no bytes arrive for d.
type idleReader struct {
	r     io.Reader
	d     time.Duration
	timer *time.Timer
	fired atomic.Bool
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.timer.Reset(i.d)
	}
	return n, err
}

// forwarded are the client headers sent on to providers; credentials and
// anything else stay behind. X-Stainless-* (SDK details) go too.
var forwarded = []string{"Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta", "Openai-Beta", "Idempotency-Key", "User-Agent"}

// anthropicVersion is sent when a client calls the Anthropic format without
// naming a version, which Anthropic requires.
const anthropicVersion = "2023-06-01"

// upstreamHeader sets the headers of a call to a provider speaking f. A
// translated call keeps none of its own format's headers.
func upstreamHeader(dst, src http.Header, f wire.Format, target http.Header) {
	for _, k := range forwarded {
		if strings.HasPrefix(k, "Anthropic-") && !f.Anthropic() || k == "Openai-Beta" && f.Anthropic() {
			continue
		}
		if v := src.Values(k); len(v) > 0 {
			dst[k] = v
		}
	}
	for k, v := range src {
		if strings.HasPrefix(k, "X-Stainless-") {
			dst[k] = v
		}
	}
	if f.Anthropic() && dst.Get("Anthropic-Version") == "" {
		dst.Set("Anthropic-Version", anthropicVersion)
	}
	if dst.Get("Content-Type") == "" {
		dst.Set("Content-Type", "application/json")
	}
	// Uncompressed, so usage can be read on the way.
	dst.Set("Accept-Encoding", "identity")
	for k, v := range target {
		dst[k] = v
	}
}

// dropped response headers: hop-by-hop ones, and those that speak for the
// provider's host rather than the gateway's.
var dropped = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true, "Te": true,
	"Trailer": true, "Transfer-Encoding": true, "Upgrade": true, "Set-Cookie": true, "Alt-Svc": true,
	"Strict-Transport-Security": true, "Server": true,
}

func responseHeader(dst, src http.Header) {
	for k, v := range src {
		if !dropped[k] {
			dst[k] = v
		}
	}
}
