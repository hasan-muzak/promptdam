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
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/redact"

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
	// evaluation, or the project's payloads) until it grows past
	// MaxSampledAnswer; capture keeps it whatever the call's status.
	sample, overflow, capture bool
	answer                    []byte
	// filter rewrites the provider's answer (Route.Response).
	filter ResponseFilter
	// promptKey is the conversation's key for OpenAI's prompt cache
	// (Route.PromptKey); promptTTL the family's learned lifetimes.
	promptKey string
	promptTTL PromptTTL
	// session is the conversation's key for OpenRouter (Route.Session).
	session string
	// rewrote reports a body the gateway changed before routing it (the
	// firewall's): a thinking block it invalidates is healed, not refused.
	rewrote bool
	// wait is how long the next try of the same target waits: a provider's
	// Retry-After, when it named one.
	wait time.Duration
	// streamFailed reports a provider error event in an answer that began
	// fine (an overload mid-stream): a failure, for the target's circuit.
	streamFailed bool
	// abort cuts the client's connection once the call is logged: an
	// answer that broke off after it began must not look complete.
	abort bool
	// answerModel is the model the answer names when the gateway moved the
	// call to another (the one the client asked for), "" to leave the
	// provider's; servedInAnswer keeps the provider's (X-Promptdam-Answer-
	// Model: served).
	answerModel    string
	servedInAnswer bool
}

// outcome is what an attempt leaves the call to do.
type outcome int

const (
	// answered: the client got an answer, or an error, and the call ends.
	answered outcome = iota
	// again: the same target may answer if asked again.
	again
	// onward: the next target may answer; this one won't.
	onward
)

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
	c.servedInAnswer = strings.EqualFold(c.r.Header.Get(brand.HeaderAnswerModel), "served")

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
	if route.Sample || route.Capture {
		// The request as the provider gets it (the firewall's, when it
		// masked it); buf goes back to its pool.
		c.rec.Body = bytes.Clone(body)
		c.sample, c.capture = route.Sample || route.CaptureAnswer, route.Capture
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
				if !c.servedInAnswer && route.Targets[0].Model != req.Model {
					c.answerModel = req.Model
				}
				c.serveCached(e)
				return
			}
			c.cacheKey, c.cacheTTL, c.rec.Cache = key, route.CacheTTL, "miss"
		}
	}

	c.promptCache, c.promptKey, c.promptTTL, c.rewrote = route.PromptCache, route.PromptKey, route.PromptTTL, route.Body != nil
	c.session = route.Session

	// Token counts come in streams only when asked for: ask on the
	// client's behalf, and leave the extra chunk out of what it receives.
	dropUsage := false
	if c.ep.format == wire.OpenAIChat && req.Stream && !req.IncludeUsage {
		if withUsage, err := wire.WithStreamUsage(body); err == nil {
			body, dropUsage = withUsage, true
		}
	}

	// Each target is tried, and retried after a jittered pause (or the
	// provider's Retry-After), until one answers; a target whose circuit is
	// open is skipped, but for the last. An attempt may send the call on
	// to the next target at once, when asking this one again can't help.
	retries := max(route.Retries, 0)
	for i, t := range route.Targets {
		lastTarget := i == len(route.Targets)-1
		if !lastTarget && !c.h.breakers.allow(t.ID, time.Now()) {
			continue
		}
		for try := 0; try <= retries; try++ {
			if try > 0 && !pause(c.r.Context(), max(backoff(try), c.wait)) {
				c.rec.Status, c.rec.ErrorCode = statusClientClosed, "client_closed"
				return
			}
			c.wait = 0
			c.rec.Attempts++
			c.rec.Fallback = i > 0
			// A call no provider answered is logged with the one it tried
			// last: provider outages show in the log.
			c.rec.Provider, c.rec.Kind, c.rec.TargetModel, c.rec.ServedModel = t.Provider, t.Kind, t.Model, t.Model
			next, failed := c.attempt(t, req, body, dropUsage, lastTarget, try == retries)
			c.h.breakers.record(t.ID, !failed, time.Now())
			if next == answered {
				return
			}
			if next == onward {
				break
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

// maxRetryAfter is the longest Retry-After the gateway waits out before
// asking the same target again; past it, the next target is asked, or the
// client gets the provider's answer, Retry-After included.
const maxRetryAfter = 10 * time.Second

// retryAfter is how long a provider asked to be left alone: Retry-After-Ms
// (OpenAI), else Retry-After in seconds or as a date.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if v := h.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	v := h.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
		return time.Duration(s * float64(time.Second)), true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

// tooLongPhrases are how providers say a call doesn't fit the model's
// context window (Anthropic's "prompt is too long", OpenAI's
// context_length_exceeded, and the wordings of OpenAI-compatible ones).
var tooLongPhrases = []string{
	"prompt is too long", "context_length_exceeded", "maximum context length", "context window",
	"exceed context limit", "exceeds the context", "input is too long", "too many input tokens",
}

// tooLong reports a provider's refusal of a call too long for its model: a
// stronger tier's model, later among the targets, may take it.
func tooLong(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge {
		return false
	}
	lower := bytes.ToLower(body)
	for _, p := range tooLongPhrases {
		if bytes.Contains(lower, []byte(p)) {
			return true
		}
	}
	return false
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

// attempt sends the call to one target and relays the answer. It says what
// the call does next: nothing more (answered), the same target again, or
// the next one. Before anything reached the client, a failure may go on
// when the target isn't the last (lastTarget) or has tries left (lastTry).
// failed reports a failure of the provider (unreachable, rate limiting,
// overloaded, a server error, an error mid-stream), for its circuit.
func (c *call) attempt(t Target, req wire.Request, body []byte, dropUsage, lastTarget, lastTry bool) (next outcome, failed bool) {
	canAgain, canOnward := !lastTarget || !lastTry, !lastTarget
	format, path := t.formatOf(c.ep.format), c.ep.path
	c.rec.PromptCached = false
	// A call moved to another model answers as the model asked for:
	// agents keep state (thinking) by the model they called.
	c.answerModel = ""
	if !c.servedInAnswer && t.Model != req.Model {
		c.answerModel = req.Model
	}
	var dropped []string
	switch {
	case format != c.ep.format:
		// A target of the other API format gets the call translated; one
		// that can't express it is passed over for the next (translating
		// again would fail the same way).
		out, d, err := translate.Request(c.ep.format, format, body,
			translate.Options{Model: t.Model, Kind: t.Kind, Reasoning: t.Reasoning, MaxTokens: t.MaxTokens})
		if err != nil {
			if canOnward {
				return onward, false
			}
			msg := "the request can't be sent to " + t.Model
			var te *translate.Error
			if errors.As(err, &te) {
				msg += ": " + te.Message
			}
			c.fail(&Error{Status: http.StatusBadRequest, Type: TypeInvalidRequest, Code: "untranslatable_request", Message: msg})
			return answered, false
		}
		body, dropped, path, dropUsage = out, d, paths[format], false
	case t.Model != req.Model:
		model, _ := json.Marshal(t.Model)
		swapped, err := wire.SetField(body, "model", model)
		if err != nil {
			c.fail(&Error{Status: http.StatusBadRequest, Type: TypeInvalidRequest, Code: "invalid_request", Message: "the request body is not valid JSON"})
			return answered, false
		}
		body = swapped
	}
	var effortDropped bool
	body, c.rec.Effort, effortDropped = ApplyEffort(body, format, t, t.Model != req.Model)
	if effortDropped {
		dropped = append(dropped, "effort")
	}
	if t.NoSampling && t.Model != req.Model {
		// Set for the model asked for; the one serving refuses them.
		var gone []string
		body, gone = translate.StripSampling(body)
		dropped = append(dropped, gone...)
	}
	c.rec.CacheStrategy, c.rec.CacheTrial, c.rec.Pooled = "", false, t.Pooled
	if t.Kind == "openrouter" && format == wire.OpenAIChat {
		body = withUsageAccounting(body)
	}
	// What the call is without the gateway's breakpoints and session, for
	// a provider that refuses them.
	plain := body
	if c.promptCache {
		body = c.cachePrompt(body, format, t, req)
	}
	ctx, cancel := context.WithCancel(c.r.Context())
	defer cancel()
	// Whether the whole request reached the provider: one that did may be
	// answered (and billed) even when its answer never arrives.
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(i httptrace.WroteRequestInfo) {
		if i.Err == nil {
			wrote.Store(true)
		}
	}})
	// A thinking block a firewall rewrite invalidated (Anthropic refuses a
	// conversation whose history changed under it) is healed once: sent
	// again with the block dropped.
	var res *http.Response
	var err error
	beta := ""
	// A provider that refuses the gateway's breakpoints or session gets the
	// call once without them (unmarking); when that answers, the strategy
	// is switched off for the model (Config.CacheRefused).
	healing, unmarking, session := true, c.promptCache, c.promptCache
	kill := false
	// The backends its model never caches on are avoided (withAvoided),
	// until none is left to serve the call.
	avoiding := c.promptCache && CacheDialectOf(t.Kind, false).Avoids && format == wire.OpenAIChat
	target := UpstreamURL(t, format, path, req.Stream)
	cloudNoted := false
	for {
		send, avoided := body, false
		if avoiding {
			send, avoided = withAvoided(body, t.Cache.Avoid)
		}
		// A cloud's own dialect (Bedrock's Converse): what it can't carry
		// is listed, what it can't express at all is passed on to the next
		// target, as a translation is.
		send, gone, cerr := CloudBody(t, format, path, send)
		if cerr != nil {
			if canOnward {
				return onward, false
			}
			msg := "the request can't be sent to " + t.Model
			var te *translate.Error
			if errors.As(cerr, &te) {
				msg += ": " + te.Message
			}
			c.fail(&Error{Status: http.StatusBadRequest, Type: TypeInvalidRequest, Code: "untranslatable_request", Message: msg})
			return answered, false
		}
		if !cloudNoted {
			dropped, cloudNoted = append(dropped, gone...), true
		}
		up, rerr := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(send))
		if rerr != nil {
			c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_misconfigured", Message: "the provider's base URL is invalid"})
			return answered, false
		}
		upstreamHeader(up.Header, c.r.Header, format, t.Header)
		sessioned := sessionHeader(up.Header, c.r.Header, body, t.Kind, c.session, session && promptcache.Sessions(t.Cache.Strategy))
		if beta != "" {
			addBeta(up.Header, beta)
		}
		CloudHeader(t, format, up.Header, req.Stream)
		if serr := Sign(ctx, t, up, send); serr != nil {
			c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_auth_failed",
				Message: "the provider's credentials couldn't sign the call: " + serr.Error()})
			return answered, false
		}
		sent := time.Now()
		res, err = c.h.cfg.Transport.RoundTrip(up)
		c.rec.Upstream += time.Since(sent)
		if err == nil {
			CloudResponse(t, format, res)
		}
		thinking := healing && c.rewrote && format == wire.AnthropicMessages
		marked := unmarking && (c.rec.PromptCached || sessioned)
		refusable := res != nil && (res.StatusCode == http.StatusBadRequest && (thinking || marked) ||
			avoided && (res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusNotFound))
		if err != nil || !refusable || eventStream(res.Header) {
			break
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		_ = res.Body.Close()
		if avoided && refusedAvoided(raw) {
			avoiding = false
			c.rec.Healed = heals(c.rec.Healed, HealUnavoided)
			continue
		}
		if thinking {
			if healed, how, needs, ok := healThinking(body, raw); ok {
				body, beta, c.rec.Healed, healing = healed, needs, heals(c.rec.Healed, how), false
				if p, _, _, ok := healThinking(plain, raw); ok {
					plain = p
				}
				continue
			}
		}
		if refused := refusedCaching(raw); marked && res.StatusCode == http.StatusBadRequest && refused != refusedNothing {
			kill = refused == refusedMarkers && c.rec.PromptCached && promptcache.Marks(c.rec.CacheStrategy) &&
				t.Pooled && killsMarkers(t.Kind, format == wire.AnthropicMessages)
			// Sent as no strategy: what it reads teaches no profile.
			body, unmarking, session, c.rec.PromptCached = plain, false, false, false
			c.rec.CacheStrategy, c.rec.CacheTrial = "", false
			c.rec.Healed = heals(c.rec.Healed, HealUnmarked)
			continue
		}
		res.Body = io.NopCloser(bytes.NewReader(raw))
		break
	}
	if kill && err == nil && res.StatusCode < http.StatusBadRequest && c.h.cfg.CacheRefused != nil {
		c.h.cfg.CacheRefused(t.Kind, t.Model, promptcache.Markers)
	}
	if err != nil {
		if c.r.Context().Err() != nil {
			c.rec.Status, c.rec.ErrorCode = statusClientClosed, "client_closed"
			return answered, false
		}
		// A call the provider never got is safe to send again; one it got
		// is never sent to it twice (it may be answering, and billing,
		// still), only on to the next target.
		sentAll := wrote.Load()
		switch {
		case !sentAll && canAgain:
			return again, true
		case canOnward:
			return onward, true
		}
		e := &Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_unreachable", Message: "the provider could not be reached"}
		switch {
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			e.Status, e.Code, e.Message = http.StatusGatewayTimeout, "provider_timeout", "the provider did not answer in time"
		case sentAll:
			e.Code, e.Message = "provider_disconnected", "the provider closed the connection before answering"
		}
		c.fail(e)
		return answered, true
	}
	// The provider's body is closed, not the masked copy that may replace it.
	defer func(b io.Closer) { _ = b.Close() }(res.Body)
	if retryable(res.StatusCode) {
		// A rate limit naming a short wait is waited out; a long one sends
		// the call on, or to the client with the provider's Retry-After.
		wait, short := time.Duration(0), true
		if res.StatusCode == http.StatusTooManyRequests {
			if d, ok := retryAfter(res.Header, time.Now()); ok {
				wait, short = d, d <= maxRetryAfter
			}
		}
		switch {
		case short && canAgain:
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
			c.wait = wait
			return again, true
		case canOnward:
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
			return onward, true
		}
	}
	if res.StatusCode >= 400 && !eventStream(res.Header) {
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			// A provider refusing the project's key may quote part of it:
			// the client gets the gateway's own error instead.
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
			c.fail(&Error{Status: http.StatusBadGateway, Type: TypeAPI, Code: "provider_auth_failed",
				Message: "the provider refused the project's key or account for it (" + strconv.Itoa(res.StatusCode) +
					"): check the provider's key in Promptdam"})
			return answered, false
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		// A call too long for this model goes on to the next target: the
		// stronger tiers' models come later among them. Not a failure of
		// the provider.
		if canOnward && tooLong(res.StatusCode, raw) {
			return onward, false
		}
		maskError(res, raw)
	}
	c.relay(res, t, cancel, dropUsage, format, dropped)
	return answered, retryable(res.StatusCode) || c.streamFailed
}

// ApplyEffort sends a call to t with t's reasoning effort, in the target's
// dialect (body is in format f, t's): a router tier's effort replaces the
// client's, except that the model's own default needn't be written when
// the client set none. A call moved to another model (moved) that is known
// not to reason has the client's reasoning_effort taken out: it was meant
// for the model asked for. applied is the effort set ("" for none);
// dropped reports a tier's effort the target's provider has no known way
// to take.
func ApplyEffort(body []byte, f wire.Format, t Target, moved bool) (out []byte, applied string, dropped bool) {
	if t.NoReasoning {
		if moved && !f.Anthropic() {
			body, _ = translate.StripEffort(body)
		}
		return body, "", false
	}
	if t.Effort == "" || t.Effort == t.DefaultEffort && !translate.HasEffort(body, f, t.Kind) {
		return body, "", false
	}
	out, ok, err := translate.SetEffort(body, f, t.Kind, t.Effort)
	if err != nil || !ok {
		return body, "", err == nil
	}
	return out, t.Effort, false
}

// errorRedactor masks secrets and personal data in providers' errors.
var errorRedactor = redact.Default()

// maxErrorBody bounds the provider error bodies read to be masked.
const maxErrorBody = 1 << 20

// maskError replaces a provider's error body, read up to maxErrorBody, with
// it masked: keys, tokens, personal data from the request it may quote.
func maskError(res *http.Response, body []byte) {
	masked, _ := errorRedactor.Mask(string(body))
	res.Body = io.NopCloser(strings.NewReader(masked))
	res.ContentLength = int64(len(masked))
	res.Header.Del("Content-Length")
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
	if translating || c.filter != nil || c.answerModel != "" {
		h.Del("Content-Length") // the answer is rewritten
	}
	if len(dropped) > 0 {
		h.Set(brand.HeaderDropped, strings.Join(dropped, ","))
	}
	if c.rec.Effort != "" {
		h.Set(brand.HeaderEffort, c.rec.Effort)
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
			if st != nil && c.answerModel != "" {
				st = translate.Named(st, c.answerModel)
			}
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
			if c.answerModel != "" {
				ev = namedEvent(ev, c.answerModel)
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
	case c.filter != nil || c.answerModel != "" && res.StatusCode < 300:
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
	c.rec.Backend = meter.Backend()
	if c.rec.ErrorCode != "" {
		return // the gateway answered with its own error
	}
	// An error event in an answer that began fine (an overload mid-stream)
	// counts against the target's circuit, unless it blames the request.
	if se := meter.StreamError(); se != "" && se != "invalid_request_error" {
		c.streamFailed = true
	}
	// An answer that broke off after its status went out is cut, not
	// ended: a clean end would make the part the client got (or a bare
	// 200) look complete. Answers relayed whole failed with the gateway's
	// own error above instead.
	if (readErr != nil || body.fired.Load()) && c.r.Context().Err() == nil {
		c.abort = true
	}
	if c.sample && !c.overflow && (c.capture || res.StatusCode == http.StatusOK && readErr == nil) {
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
		out = named(out, c.answerModel)
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
	c.write(named(c.filterBody(kept), c.answerModel))
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

// named is a whole answer naming model ("" leaves it): its top-level
// "model", when it has one.
func named(body []byte, model string) []byte {
	if model == "" {
		return body
	}
	if _, ok, err := wire.Field(body, "model"); err != nil || !ok {
		return body
	}
	v, _ := json.Marshal(model)
	out, err := wire.SetField(body, "model", v)
	if err != nil {
		return body
	}
	return out
}

// namedEvent is a streamed event naming model: a Chat Completions chunk's
// model, a Messages stream's message_start's message.model. Other events
// name none.
func namedEvent(ev wire.Event, model string) wire.Event {
	if ev.Partial || len(ev.Data) == 0 || ev.Data[0] != '{' {
		return ev
	}
	data := ev.Data
	switch ev.Name {
	case "message_start":
		msg, ok, err := wire.Field(data, "message")
		if err != nil || !ok {
			return ev
		}
		if data, err = wire.SetField(data, "message", named(msg, model)); err != nil {
			return ev
		}
	case "":
		data = named(data, model)
	default:
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
	h.Del("Content-Length")
	c.w.WriteHeader(e.Status)
	c.write(named(c.filterBody(e.Body), c.answerModel))
	if c.capture && c.sample && !c.overflow {
		c.rec.Answer = c.answer
	}
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

// dropped response headers: hop-by-hop ones, those that speak for the
// provider's host rather than the gateway's, and those naming the
// customer's account at the provider.
var dropped = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true, "Te": true,
	"Trailer": true, "Transfer-Encoding": true, "Upgrade": true, "Set-Cookie": true, "Alt-Svc": true,
	"Strict-Transport-Security": true, "Server": true,
	"Openai-Organization": true, "Openai-Project": true, "Anthropic-Organization-Id": true,
}

// responseHeader copies a provider's headers, but for the dropped ones and
// the gateway's own (X-Promptdam-…), which a provider can't speak for.
func responseHeader(dst, src http.Header) {
	for k, v := range src {
		if !dropped[k] && !strings.HasPrefix(k, brand.HeaderPrefix) {
			dst[k] = v
		}
	}
}

// withUsageAccounting asks OpenRouter to report what each call cost
// (usage.include), unless the client set usage itself. The field goes at
// the body's end: the prompt's bytes, which its providers cache, don't
// move.
func withUsageAccounting(body []byte) []byte {
	if _, found, err := wire.Field(body, "usage"); err != nil || found {
		return body
	}
	if out, err := wire.SetField(body, "usage", []byte(`{"include":true}`)); err == nil {
		return out
	}
	return body
}
