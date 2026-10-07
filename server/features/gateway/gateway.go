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

	"github.com/hasan-muzak/keel/pkg/llm/wire"
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
}

// Route is where a call goes.
type Route struct {
	// Targets are tried in order until one answers: the first is the
	// model asked for (or the one a rule chose), the others fallbacks.
	Targets []Target
	// Meta is the resolver's own data about the call (the caller, its
	// project…), handed back in the Record.
	Meta any
}

// Target is one provider and model to send a call to.
type Target struct {
	// Provider names the provider, e.g. "openai", for logs and costs.
	Provider string
	// BaseURL is the provider API's root, the part before the endpoint
	// path: https://api.openai.com/v1, https://api.anthropic.com/v1.
	BaseURL string
	// Model is the model to ask for; the body is rewritten when it differs
	// from the requested one.
	Model string
	// Header holds the provider's credentials and any fixed headers.
	Header http.Header
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

// Record is what happened to one call, for the request log and the ledger.
type Record struct {
	Trace  string
	Format wire.Format
	// Meta is the Route's Meta (nil when the call wasn't resolved).
	Meta           any
	Provider       string
	RequestedModel string
	// ServedModel is the model the provider says answered, else the one
	// asked for.
	ServedModel string
	Stream      bool
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
}

// Handler serves the gateway's endpoints.
type Handler struct {
	cfg Config
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
	return &Handler{cfg: cfg}
}

// ServeHTTP serves one call.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
