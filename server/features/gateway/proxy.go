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

	"github.com/hasan-muzak/keel/pkg/llm/wire"

	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/families"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/promptcache"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/responsecache"
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
		Shape: c.rec.Shape, BodyBytes: len(body),
	})
	if err != nil {
		c.fail(err)
		return
	}
	c.rec.Meta = route.Meta
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
	if t.Model != req.Model {
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
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+c.ep.path, bytes.NewReader(body))
	if err != nil {
		c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_misconfigured", Message: "the provider's base URL is invalid"})
		return true, false
	}
	upstreamHeader(up.Header, c.r.Header, c.ep.format, t.Header)

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
	c.relay(res, t, cancel, dropUsage)
	return true, retryable(res.StatusCode)
}

// relay streams a provider's response to the client, reading its usage.
func (c *call) relay(res *http.Response, t Target, cancel context.CancelFunc, dropUsage bool) {
	c.rec.Provider, c.rec.Kind, c.rec.ServedModel, c.rec.Status = t.Provider, t.Kind, t.Model, res.StatusCode
	c.rec.TargetModel = t.Model
	h := c.w.Header()
	responseHeader(h, res.Header)
	h.Set(brand.HeaderTrace, c.rec.Trace)
	h.Set(brand.HeaderProvider, t.Provider)
	h.Set(brand.HeaderServedModel, t.Model)
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

	meter := wire.NewMeter(c.ep.format)
	rc := http.NewResponseController(c.w)
	var readErr error
	if eventStream(res.Header) {
		h.Del("Content-Length")
		c.w.WriteHeader(res.StatusCode)
		_ = rc.Flush()
		s := wire.NewScanner(body, 0)
		for s.Next() {
			ev := s.Event()
			if meter.Event(ev) && dropUsage {
				continue
			}
			if !c.write(ev.Raw) {
				break
			}
			_ = rc.Flush()
		}
		readErr = s.Err()
	} else {
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
				c.store(res, t, meter, kept)
			}
		}
	}

	c.rec.Usage, c.rec.UsageKnown = meter.Usage()
	if m := meter.Model(); m != "" {
		c.rec.ServedModel = m
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

// serveCached answers from the response cache.
func (c *call) serveCached(e *responsecache.Entry) {
	h := c.w.Header()
	h.Set("Content-Type", e.ContentType)
	h.Set(brand.HeaderTrace, c.rec.Trace)
	h.Set(brand.HeaderProvider, e.Provider)
	h.Set(brand.HeaderServedModel, e.Model)
	h.Set(brand.HeaderCache, "hit")
	c.w.WriteHeader(e.Status)
	c.write(e.Body)
	c.rec.Status, c.rec.Cache = e.Status, "hit"
	c.rec.Provider, c.rec.Kind, c.rec.ServedModel, c.rec.TargetModel = e.Provider, e.Kind, e.Model, c.rec.RequestedModel
	c.rec.Usage, c.rec.UsageKnown = e.Usage, true
}

// store caches a successful answer, in the background.
func (c *call) store(res *http.Response, t Target, meter *wire.Meter, body []byte) {
	if len(body) > responsecache.MaxEntry {
		return
	}
	usage, _ := meter.Usage()
	model := meter.Model()
	if model == "" {
		model = t.Model
	}
	e := &responsecache.Entry{Status: res.StatusCode, ContentType: res.Header.Get("Content-Type"), Body: bytes.Clone(body),
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

func upstreamHeader(dst, src http.Header, f wire.Format, target http.Header) {
	for _, k := range forwarded {
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
