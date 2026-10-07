// Package resolver decides, for every gateway call, who is calling and
// where the call goes, from memory: the API key (an in-process cache in
// front of keel's), the project's snapshot, the key's settings, limits and
// budgets, then the route and the providers serving the model.
package resolver

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/apikeys"
	"github.com/hasan-muzak/keel/pkg/counter"
	"github.com/hasan-muzak/keel/pkg/llm/wire"
	"github.com/hasan-muzak/keel/pkg/memcache"
	"github.com/hasan-muzak/keel/pkg/metering"

	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
	"github.com/hasan-muzak/keel/products/promptdam/server/catalog"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/budgets"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/gateway"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/prices"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/routes"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/snapshot"
)

// KeyTTL is how long a gateway trusts a key it checked: a revoked key
// works on a gateway for at most this long.
const KeyTTL = 5 * time.Second

// Meta is who made a call, handed back in the gateway's Record.
type Meta struct {
	Org, Project, Key uuid.UUID
	Developer         string
	// User is the end user the client named (X-Promptdam-User).
	User  string
	Tags  []string
	Route string
	// Prices are the project's own prices, over the price list.
	Prices prices.Project
}

// Who is what the call's spend counts against.
func (m *Meta) Who() budgets.Who {
	return budgets.Who{Project: m.Project, Key: m.Key, Developer: m.Developer, Route: m.Route}
}

// Authenticator checks API keys (apikeys.Service).
type Authenticator interface {
	AuthenticateFrom(ctx context.Context, raw, client string) (apikeys.Principal, error)
}

// Quota counts plan usage (metering.Service).
type Quota interface {
	Use(ctx context.Context, org uuid.UUID, meter string, n int64) (metering.Decision, error)
}

// Config configures a Resolver.
type Config struct {
	Keys      Authenticator
	Snapshots *snapshot.Store
	Quota     Quota
	Spend     *budgets.Spend
	// Rates counts requests per key and minute (counter "rpm").
	Rates  *counter.Counters
	Prices prices.Prices
	// Scope is the scope a key needs.
	Scope string
}

// Resolver implements gateway.Resolver and gateway.ModelLister.
type Resolver struct {
	cfg  Config
	keys *memcache.Cache[[32]byte, apikeys.Principal]
}

// New returns a Resolver.
func New(cfg Config) *Resolver {
	return &Resolver{cfg: cfg, keys: memcache.New[[32]byte, apikeys.Principal](KeyTTL, KeyTTL, 100_000)}
}

func refuse(status int, typ, code, msg string) *gateway.Error {
	return &gateway.Error{Status: status, Type: typ, Code: code, Message: msg}
}

// rawKey is the API key a client sent: a bearer token (OpenAI SDKs) or
// x-api-key (Anthropic's).
func rawKey(h http.Header) string {
	if k := h.Get("X-Api-Key"); k != "" {
		return k
	}
	if a := h.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	return ""
}

// principal authenticates the call's key.
func (r *Resolver) principal(ctx context.Context, c *gateway.Call) (apikeys.Principal, error) {
	raw := rawKey(c.Header)
	if raw == "" {
		return apikeys.Principal{}, refuse(http.StatusUnauthorized, gateway.TypeAuthentication, "missing_api_key",
			"send your "+brand.Name+" API key as a bearer token or in x-api-key")
	}
	if len(raw) > 200 {
		return apikeys.Principal{}, refuse(http.StatusUnauthorized, gateway.TypeAuthentication, "invalid_api_key", "the API key is not valid")
	}
	p, err := r.keys.Get(sha256.Sum256([]byte(raw)), time.Now(), func() (apikeys.Principal, error) {
		p, err := r.cfg.Keys.AuthenticateFrom(ctx, raw, c.Client)
		if err != nil && !errors.Is(err, apikeys.ErrInvalidKey) {
			// A held-back client or a store down says nothing about the key.
			return p, memcache.Uncached{Err: err}
		}
		return p, err
	})
	var throttled *apikeys.ThrottledError
	switch {
	case errors.Is(err, apikeys.ErrInvalidKey):
		return p, refuse(http.StatusUnauthorized, gateway.TypeAuthentication, "invalid_api_key", "the API key is not valid")
	case errors.As(err, &throttled):
		e := refuse(http.StatusTooManyRequests, gateway.TypeRateLimit, "too_many_invalid_keys", "too many invalid API keys from this address")
		e.RetryAfter = throttled.RetryAfter
		return p, e
	case err != nil:
		return p, refuse(http.StatusServiceUnavailable, gateway.TypeOverloaded, "unavailable", "the gateway can't check keys right now")
	}
	if !slices.Contains(p.Scopes, r.cfg.Scope) || p.ProjectID == nil {
		return p, refuse(http.StatusForbidden, gateway.TypePermission, "key_not_for_gateway", "the API key can't call the gateway")
	}
	return p, nil
}

// caller is the authenticated caller with its project's snapshot.
func (r *Resolver) caller(ctx context.Context, c *gateway.Call) (*Meta, *snapshot.Snapshot, error) {
	p, err := r.principal(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	snap, err := r.cfg.Snapshots.Get(ctx, p.OrgID, *p.ProjectID)
	if err != nil {
		return nil, nil, refuse(http.StatusServiceUnavailable, gateway.TypeOverloaded, "unavailable",
			"the gateway can't read the project's configuration right now")
	}
	settings := snap.Keys[p.KeyID]
	m := &Meta{Org: p.OrgID, Project: *p.ProjectID, Key: p.KeyID, Developer: settings.Developer, Prices: snap.Prices}
	if u := strings.TrimSpace(c.Header.Get(brand.HeaderUser)); len(u) <= 200 {
		m.User = u
	}
	m.Tags = append(slices.Clone(settings.Tags), headerTags(c.Header.Get(brand.HeaderTags))...)
	return m, snap, nil
}

// headerTags are the tags a client sent: at most 10, each at most 64
// characters.
func headerTags(v string) []string {
	var out []string
	for t := range strings.SplitSeq(v, ",") {
		if t = strings.TrimSpace(t); t != "" && len(t) <= 64 && len(out) < 10 {
			out = append(out, t)
		}
	}
	return out
}

// Resolve implements gateway.Resolver.
func (r *Resolver) Resolve(ctx context.Context, c *gateway.Call) (*gateway.Route, error) {
	m, snap, err := r.caller(ctx, c)
	if err != nil {
		return nil, err
	}
	settings := snap.Keys[m.Key]
	model := c.Request.Model
	if !settings.Allows(model) {
		return nil, refuse(http.StatusForbidden, gateway.TypePermission, "model_not_allowed",
			"the API key may not call model "+model)
	}

	route, err := pickRoute(c.Header.Get(brand.HeaderRoute), settings.Route, snap)
	if err != nil {
		return nil, err
	}
	m.Route = route.Name

	if settings.RPM > 0 {
		ok, err := r.cfg.Rates.Allow(ctx, m.Project.String()+":"+m.Key.String(), counter.Minute, 1, int64(settings.RPM))
		if err == nil && !ok {
			e := refuse(http.StatusTooManyRequests, gateway.TypeRateLimit, "rate_limited",
				"the API key's "+strconv.Itoa(settings.RPM)+" requests per minute are used up")
			e.RetryAfter = time.Until(counter.Minute.End(counter.Minute.Start(time.Now())))
			return nil, e
		}
	}
	if b, until, err := r.cfg.Spend.Exceeded(ctx, m.Who(), snap.Budgets); err == nil && b != nil {
		e := refuse(http.StatusTooManyRequests, gateway.TypeRateLimit, "budget_exceeded",
			"a budget of the "+b.Scope+" is spent for this "+b.Window)
		e.RetryAfter = time.Until(until)
		return nil, e
	}
	format := providers.FormatOpenAI
	if c.Format.Anthropic() {
		format = providers.FormatAnthropic
	}
	targets := r.targets(snap, format, model, nil)
	if len(targets) == 0 {
		return nil, refuse(http.StatusNotFound, gateway.TypeNotFound, "no_provider",
			"no provider of the project serves model "+model+" in the "+format+" API format: add one, or list the model on a provider")
	}
	for _, f := range route.Fallbacks {
		targets = append(targets, r.fallback(snap, format, f, targets)...)
	}
	// The plan's requests, counted once a call can go somewhere: hard on
	// Free. If usage can't be counted, the call goes through.
	if d, err := r.cfg.Quota.Use(ctx, m.Org, catalog.Requests, 1); err == nil && !d.Allowed() {
		e := refuse(http.StatusTooManyRequests, gateway.TypeRateLimit, "quota_exceeded",
			"the organization's plan has no requests left this month")
		e.RetryAfter = time.Hour
		return nil, e
	}
	out := &gateway.Route{Targets: targets, Retries: route.Retries, PromptCache: route.PromptCache, Meta: m}
	if route.CacheTTL > 0 {
		out.CacheTTL = time.Duration(route.CacheTTL) * time.Second
		out.CacheScope = m.Project.String() + ":" + route.Name
	}
	return out, nil
}

// pickRoute is the route a call takes: the one it names, else its key's,
// else the project's default; none is a route without fallbacks.
func pickRoute(named, keys string, snap *snapshot.Snapshot) (snapshot.Route, error) {
	if named != "" {
		r, ok := snap.Routes[named]
		if !ok {
			return snapshot.Route{}, refuse(http.StatusBadRequest, gateway.TypeInvalidRequest, "unknown_route",
				"the project has no route named "+strconv.Quote(named))
		}
		return r, nil
	}
	if r, ok := snap.Routes[keys]; keys != "" && ok {
		return r, nil
	}
	if r, ok := snap.Routes[routes.Default]; ok {
		return r, nil
	}
	return snapshot.Route{Retries: 1}, nil
}

// targets are the providers serving model in format, by priority, minus
// those already tried.
func (r *Resolver) targets(snap *snapshot.Snapshot, format, model string, skip []gateway.Target) []gateway.Target {
	var out []gateway.Target
	for _, p := range snap.Providers {
		if p.Format != format || !r.serves(p.Provider, model) || tried(skip, p.Name, model) {
			continue
		}
		out = append(out, gateway.Target{ID: p.ID.String() + ":" + model, Provider: p.Name, Kind: p.Kind, BaseURL: p.BaseURL,
			Model: model, Header: p.Header})
	}
	return out
}

// fallback resolves a route's fallback: provider/model when a provider of
// the project has that name, else a model any serving provider answers.
func (r *Resolver) fallback(snap *snapshot.Snapshot, format, f string, skip []gateway.Target) []gateway.Target {
	if name, model, ok := strings.Cut(f, "/"); ok {
		for _, p := range snap.Providers {
			if p.Name == name && p.Format == format && !tried(skip, p.Name, model) {
				return []gateway.Target{{ID: p.ID.String() + ":" + model, Provider: p.Name, Kind: p.Kind, BaseURL: p.BaseURL,
					Model: model, Header: p.Header}}
			}
		}
	}
	return r.targets(snap, format, f, skip)
}

func tried(ts []gateway.Target, provider, model string) bool {
	return slices.ContainsFunc(ts, func(t gateway.Target) bool { return t.Provider == provider && t.Model == model })
}

// serves reports whether a provider answers a model: those it lists, else
// any for a custom endpoint, "vendor/model" names for OpenRouter, the
// catalog's models of its kind, or names starting like its kind's models.
func (r *Resolver) serves(p snapshot.Provider, model string) bool {
	if len(p.Models) > 0 {
		for _, m := range p.Models {
			if pre, ok := strings.CutSuffix(m, "*"); ok && strings.HasPrefix(model, pre) || m == model {
				return true
			}
		}
		return false
	}
	switch p.Kind {
	case providers.Custom:
		return true
	case "openrouter":
		return strings.Contains(model, "/")
	}
	kind, ok := providers.LookupKind(p.Kind)
	if !ok {
		return false
	}
	if m, ok := r.cfg.Prices.Current().Lookup(kind.Catalog, model); ok && m.Provider == kind.Catalog {
		return true
	}
	for _, pre := range kind.Prefixes {
		if strings.HasPrefix(model, pre) {
			return true
		}
	}
	return false
}

// Models implements gateway.ModelLister: the models the key may call, from
// its project's providers (their lists, else the catalog's models of their
// kind), in the API format the client speaks.
func (r *Resolver) Models(ctx context.Context, c *gateway.Call) ([]gateway.Model, error) {
	m, snap, err := r.caller(ctx, c)
	if err != nil {
		return nil, err
	}
	settings := snap.Keys[m.Key]
	format := providers.FormatOpenAI
	if c.Format == wire.AnthropicMessages {
		format = providers.FormatAnthropic
	}
	seen := map[string]bool{}
	var out []gateway.Model
	add := func(id, provider string) {
		if !seen[id] && !strings.HasSuffix(id, "*") && settings.Allows(id) {
			seen[id] = true
			out = append(out, gateway.Model{ID: id, Provider: provider})
		}
	}
	for _, p := range snap.Providers {
		if p.Format != format {
			continue
		}
		if len(p.Models) > 0 {
			for _, id := range p.Models {
				add(id, p.Name)
			}
			continue
		}
		if kind, ok := providers.LookupKind(p.Kind); ok && kind.Catalog != "" {
			for _, cm := range r.cfg.Prices.Current().Models {
				if cm.Provider == kind.Catalog {
					add(cm.ID, p.Name)
				}
			}
		}
	}
	return out, nil
}
