// Package gateway is Promptdam's hot path: it takes OpenAI- and
// Anthropic-format requests, asks a Resolver where each one goes, forwards
// it to the provider and streams the answer back byte for byte, reading the
// usage on the way (pkg/llm/wire). It never touches a database: the
// Resolver answers from memory, and what happened is handed to Config.Done.
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/responsecache"
)

// Resolver decides where a call goes: it authenticates the caller and picks
// the provider, its credentials and the model. It must answer from memory.
type Resolver interface {
	Resolve(ctx context.Context, c *Call) (*Route, error)
}

// Call is one request, as the resolver sees it.
type Call struct {
	Format  wire.Format
	Request wire.Request
	// Header is the client's request header, credentials included.
	Header http.Header
	// Trace identifies the call (the X-Promptdam-Trace header).
	Trace string
	// Client identifies the caller's address, for holding back clients that
	// guess keys (Config.ClientKey).
	Client string
	// Shape is the call's prompt family and how its prefix looks.
	Shape families.Shape
	// BodyBytes is the request body's size.
	BodyBytes int
	// Body is the request body, to read only: it must not be changed or
	// kept after Resolve returns.
	Body []byte
}

// Route is where a call goes.
type Route struct {
	// Targets are tried in order until one answers: the first is the
	// model asked for (or the one a rule chose), the others fallbacks.
	Targets []Target
	// Retries is how often a target is retried, with jitter, before the
	// next one.
	Retries int
	// CacheTTL, when set, answers identical non-streamed calls from the
	// response cache, in CacheScope (the project and route).
	CacheTTL   time.Duration
	CacheScope string
	// PromptCache adds Anthropic cache breakpoints to calls without any.
	PromptCache bool
	// Header is set on the response (e.g. the router's tier).
	Header http.Header
	// Sample keeps the call's request and answer in its Record (Body,
	// Answer), in memory, for a router's evaluation.
	Sample bool
	// Capture keeps the call's request in its Record (Body) for its
	// project's payloads; CaptureAnswer keeps its answer too, whatever its
	// status (Answer).
	Capture, CaptureAnswer bool
	// Body, when set, is sent instead of the client's request body (the
	// firewall's, findings masked): for the response cache's key and
	// a sample too.
	Body []byte
	// Response, when set, rewrites what the provider answers before the
	// client gets it.
	Response ResponseFilter
	// Meta is the resolver's own data about the call (the caller, its
	// project…), handed back in the Record.
	Meta any
}

// ResponseFilter rewrites a provider's answer: a whole one (Body), or a
// stream's events one by one (Event, each event's data), in the
// provider's API format; nil returns leave them as they are. End follows
// the last. A filter serves one call.
type ResponseFilter interface {
	Body(doc []byte) []byte
	Event(data []byte) []byte
	End()
}

// Target is one provider and model to send a call to.
type Target struct {
	// ID identifies the target's circuit breaker: a provider's model, as
	// one project calls it (outages and rate limits are per key).
	ID string
	// Provider names the provider, e.g. "openai" or a project's
	// "openai-backup", for logs and the X-Promptdam-Provider header.
	Provider string
	// Kind is the provider's kind ("openai", "anthropic", …), for costs.
	Kind string
	// BaseURL is the provider API's root, the part before the endpoint
	// path: https://api.openai.com/v1, https://api.anthropic.com/v1.
	BaseURL string
	// Model is the model to ask for; the body is rewritten when it differs
	// from the requested one.
	Model string
	// Header holds the provider's credentials and any fixed headers.
	Header http.Header
	// Format is the API the target is called in when it isn't the call's
	// own (0): a Chat Completions call sent to a Messages provider, or the
	// other way round, translated both ways (features/translate).
	Format wire.Format
	// Reasoning reports that the target's model takes a reasoning effort,
	// which translation passes on rather than drops.
	Reasoning bool
	// MaxTokens caps a translated Messages call that set no limit (0: the
	// translation's default).
	MaxTokens int
}

// formatOf is the API format a target is called in for a call of format f.
func (t Target) formatOf(f wire.Format) wire.Format {
	if t.Format == 0 {
		return f
	}
	return t.Format
}

// endpoint is a gateway path and the provider path it maps to.
type endpoint struct {
	format wire.Format
	path   string // under the target's BaseURL
}

var endpoints = map[string]endpoint{
	"/v1/chat/completions":      {wire.OpenAIChat, "/chat/completions"},
	"/v1/responses":             {wire.OpenAIResponses, "/responses"},
	"/v1/embeddings":            {wire.OpenAIEmbeddings, "/embeddings"},
	"/v1/messages":              {wire.AnthropicMessages, "/messages"},
	"/v1/messages/count_tokens": {wire.AnthropicCountTokens, "/messages/count_tokens"},
}

// MaxSampledAnswer bounds the answer a sampled call keeps.
const MaxSampledAnswer = 1 << 20

// paths are the provider paths of the formats calls are translated to.
var paths = map[wire.Format]string{wire.OpenAIChat: "/chat/completions", wire.AnthropicMessages: "/messages"}

// Record is what happened to one call, for the request log and the ledger.
type Record struct {
	Trace  string
	Format wire.Format
	// Meta is the Route's Meta (nil when the call wasn't resolved).
	Meta           any
	Provider       string
	Kind           string
	RequestedModel string
	// ServedModel is the model the provider says answered, else the one
	// asked for.
	ServedModel string
	Stream      bool
	// Shape is the call's prompt family and how its prefix looks.
	Shape families.Shape
	// Request is what the gateway read of the request.
	Request wire.Request
	// Fallback reports that a fallback target answered, not the first.
	Fallback bool
	// TargetModel is the model the gateway asked the provider for (the
	// provider may report a dated name for it in ServedModel).
	TargetModel string
	// Cache is how the response cache answered: hit, miss, or "" when the
	// call's route doesn't cache.
	Cache string
	// PromptCached reports that the gateway added prompt cache breakpoints.
	PromptCached bool
	// Translated reports that the call was answered in the other API
	// format, translated; Dropped lists the parameters it left out.
	Translated bool
	Dropped    []string
	// Body and Answer are a sampled or captured call's request (as the
	// provider got it) and the answer the client got (a whole response or
	// its stream, in the client's format), held in memory; Answer is nil
	// when it was larger than MaxSampledAnswer, or when a sampled call
	// failed (a captured one keeps its error).
	Body, Answer []byte
	// Status is the HTTP status the client got; ErrorCode says why a call
	// failed ("" when it didn't).
	Status    int
	ErrorCode string
	Usage     wire.Usage
	// UsageKnown is false when the provider reported no usage (a failed or
	// cut-off call).
	UsageKnown bool
	Start      time.Time
	// TTFB is the time to the first byte written to the client; Latency
	// the whole call; Upstream the time spent waiting on providers.
	TTFB, Latency, Upstream time.Duration
	// Attempts counts the targets tried.
	Attempts      int
	RequestBytes  int
	ResponseBytes int64
}

// Config configures a Handler.
type Config struct {
	Resolver Resolver
	// Transport sends upstream requests (safehttp.NewTransport).
	Transport http.RoundTripper
	// Done receives every call's record when the call ends. It runs on the
	// request's goroutine: it must not block.
	Done func(*Record)
	// MaxBody bounds request bodies (default 32 MiB, Anthropic's limit).
	MaxBody int64
	// IdleTimeout ends an upstream response that sends nothing for this
	// long (default 5 min).
	IdleTimeout time.Duration
	// NewTrace makes call IDs.
	NewTrace func() string
	// ClientKey identifies a request's client (its address), for Call.Client.
	ClientKey func(*http.Request) string
	// Cache is the response cache (nil: none).
	Cache Cache
}

// Cache keeps answers for routes that cache them (responsecache.Cache).
type Cache interface {
	Get(ctx context.Context, key string) (*responsecache.Entry, bool)
	Put(ctx context.Context, key string, e *responsecache.Entry, ttl time.Duration)
}

// Model is a model the caller may use (GET /v1/models).
type Model struct {
	ID       string
	Provider string
}

// ModelLister is a Resolver that lists the models a caller may use.
type ModelLister interface {
	Models(ctx context.Context, c *Call) ([]Model, error)
}

// Handler serves the gateway's endpoints.
type Handler struct {
	cfg      Config
	breakers *breakers
}

// New returns a Handler.
func New(cfg Config) *Handler {
	if cfg.MaxBody == 0 {
		cfg.MaxBody = 32 << 20
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}
	if cfg.Done == nil {
		cfg.Done = func(*Record) {}
	}
	if cfg.NewTrace == nil {
		cfg.NewTrace = newTrace
	}
	return &Handler{cfg: cfg, breakers: newBreakers()}
}

// ServeHTTP serves one call.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" {
		h.models(w, r)
		return
	}
	ep, ok := endpoints[r.URL.Path]
	format := ep.format
	if !ok {
		// Errors take the shape of the API the client meant to call.
		format = wire.OpenAIChat
		if strings.HasPrefix(r.URL.Path, "/v1/messages") {
			format = wire.AnthropicMessages
		}
		writeError(w, format, &Error{Status: http.StatusNotFound, Type: TypeNotFound, Code: "unknown_endpoint",
			Message: fmt.Sprintf("%s %s is not a gateway endpoint", r.Method, r.URL.Path)})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, format, &Error{Status: http.StatusMethodNotAllowed, Type: TypeInvalidRequest, Code: "method_not_allowed",
			Message: r.URL.Path + " accepts POST only"})
		return
	}
	c := &call{h: h, w: w, r: r, ep: ep, rec: Record{Trace: h.cfg.NewTrace(), Format: ep.format, Start: time.Now()}}
	c.serve()
	c.rec.Latency = time.Since(c.rec.Start)
	h.cfg.Done(&c.rec)
	if c.rec.ErrorCode != "" {
		slog.Debug("promptdam gateway: call failed", "trace", c.rec.Trace, "status", c.rec.Status, "code", c.rec.ErrorCode)
	}
}
