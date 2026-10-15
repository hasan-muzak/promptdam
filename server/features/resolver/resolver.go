// Package resolver decides, for every gateway call, who is calling and
// where the call goes, from memory: the API key (an in-process cache in
// front of keel's), the project's snapshot, the key's settings, limits and
// budgets, then the route and the providers serving the model.
package resolver

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/apikeys"
	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/counter"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/memcache"
	"github.com/hasan-muzak/keel-core/pkg/metering"
	"github.com/hasan-muzak/keel-core/pkg/policy"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/conversation"
	"github.com/hasan-muzak/promptdam/server/features/converse"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/promptdam/server/features/payloads"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
	"github.com/hasan-muzak/promptdam/server/features/rules"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
	"github.com/hasan-muzak/promptdam/server/features/translate"
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
	// Rule is the routing rule that sent the call to another model.
	Rule string
	// Router is the model router that placed the call, Tier the tier that
	// served it and Decided the one the router chose, with the step that
	// chose it and how sure it was. A shadow router decides without
	// serving: Shadow, and Tier is its default.
	Router, Tier, Decided, TierSource string
	TierConfidence                    float64
	Shadow                            bool
	// Baseline is the target savings are counted against when the call
	// named a router: its default tier's model ("" for the model asked
	// for).
	Baseline gateway.Target
	// OutputRatio is how many output tokens the router's default tier
	// writes for each one the served tier writes, measured on the router's
	// judged calls (0: not measured, or savings aren't counted against the
	// default tier): the ledger prices the counterfactual with it.
	OutputRatio float64
	// Decision is the decision model's call when one was asked: its
	// target and usage, which the call's cost includes.
	Decision struct {
		Target  gateway.Target
		Usage   wire.Usage
		Micros  int64
		Outcome string
		Took    time.Duration
	}
	// Sampled marks a call its router evaluates: the router and the
	// project's configuration the call was placed with go with it.
	Sampled bool
	Placed  *routers.Router
	Snap    *snapshot.Snapshot
	// Firewall is what the firewall made of the call's request (nil when
	// it is off), Answer what it found in the answer.
	Firewall *firewall.Outcome
	Answer   *firewall.Response
	// Capture keeps the call's payload, masked by its engine (nil: kept
	// not).
	Capture *payloads.Capture
	// Turn is the call's place in its conversation; InTurn is false for a
	// call without a human message (or of an API without turns).
	Turn   conversation.Keys
	InTurn bool
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
	// Rules is the environment routing rules are compiled in.
	Rules *policy.Env
	// Sample draws a number in [0, 1) for each router call: below the
	// router's sample rate, the call is evaluated (default rand.Float64).
	Sample func() float64
	// Entitlements says what the organization's plan includes
	// (metering.Service.Entitlements, cached in process); nil allows all.
	Entitlements func(ctx context.Context, org uuid.UUID) (billing.Entitlements, error)
	// Decider asks routers' decision models (features/decide); nil never
	// does.
	Decider Decider
	// Turns keeps what the gateway knows of each conversation's turns
	// (features/conversation); nil keeps nothing.
	Turns *conversation.Store
	// Caching plans a call's prompt caching by its model's learned profile
	// (features/cacheprofile); nil plans by the price list alone.
	Caching CachePlanner
	// Pooled reports a provider whose calls are pooled into the profiles
	// and planned by them (default providers.Public: a kind's own public
	// API); tests let their fake providers stand in.
	Pooled func(kind, baseURL string) bool
}

// CachePlanner plans how a call to a target uses its provider's prompt
// cache: def is the target's strategy by its price list, conversation the
// call's conversation key ("" outside one). ReadShare is the share of what
// a model's calls should read from its cache that they do (1 until
// measured).
type CachePlanner interface {
	Plan(kind string, anthropic bool, model, def, conversation string, inTurn bool) gateway.CachePlan
	ReadShare(kind, model string) float64
}

// Decider asks a decision model which tier a call needs.
type Decider interface {
	Decide(ctx context.Context, q Question) Decision
}

// Decision is a decision model's answer to a call.
type Decision struct {
	// Tier is the tier it picked, "" when it picked none.
	Tier string
	// Usage is what the call paid: nothing for an answer reused.
	Usage wire.Usage
	// Outcome is how the question went (Decision… constants); Took how
	// long the call waited for it, and Timeout how long it could.
	Outcome       string
	Took, Timeout time.Duration
	// Confidence is how likely the tier is enough, from a decision model's
	// probabilities (0 when the model gave none).
	Confidence float64
	// Micros is what the question cost, when the provider said (0: priced
	// from Usage).
	Micros int64
}

// How a decision model's question went, as the request log says.
const (
	DecisionAnswered   = "answered"   // the model named a tier
	DecisionCached     = "cached"     // an answer to the same question reused
	DecisionTimeout    = "timeout"    // no answer within the timeout
	DecisionError      = "error"      // the provider failed or refused
	DecisionUnreadable = "unreadable" // an answer naming no tier
)

// Question is what a decision model is asked.
type Question struct {
	// Project, Router and Family key cached answers: a project's never
	// answers another's.
	Project        uuid.UUID
	Router, Family string
	// Target is the decision model at one of the project's providers;
	// Schema reports that it takes a JSON schema for its answer.
	Target gateway.Target
	Schema bool
	Tiers  []tiers.Tier
	// Instructions and Request are the call's instructions and its last
	// user message.
	Instructions, Request string
	// Timeout bounds the wait; 0 lets the decider set it from how long
	// the model takes (an auto timeout).
	Timeout time.Duration
}

// Resolver implements gateway.Resolver and gateway.ModelLister.
type Resolver struct {
	cfg  Config
	keys *memcache.Cache[[32]byte, apikeys.Principal]
}

// New returns a Resolver.
func New(cfg Config) *Resolver {
	if cfg.Sample == nil {
		cfg.Sample = rand.Float64
	}
	if cfg.Pooled == nil {
		cfg.Pooled = providers.Public
	}
	return &Resolver{cfg: cfg, keys: memcache.New[[32]byte, apikeys.Principal](KeyTTL, KeyTTL, 100_000)}
}

// outOfRequests refuses a call the plan has no requests left for, until
// the period ends: Free's limit (or the free plan's, while a paid plan is
// restricted for an unpaid invoice) answers quota_exceeded, a paid plan's
// spend cap on overage spend_cap_reached.
func (r *Resolver) outOfRequests(ctx context.Context, org uuid.UUID) *gateway.Error {
	code, msg, fix := "quota_exceeded", "the organization's plan has no requests left", "an owner can upgrade the plan under Billing"
	var until time.Time
	if r.cfg.Entitlements != nil {
		if e, err := r.cfg.Entitlements(ctx, org); err == nil {
			until = e.PeriodEnd
			_, capped := e.Ceiling(catalog.Requests)
			switch {
			case e.Restricted:
				msg = "the organization's invoice went unpaid past its grace period, so the free plan's requests apply and none are left"
				fix = "an owner can subscribe again under Billing"
			case capped:
				code, msg = "spend_cap_reached", "the organization's overage reached its spend cap"
				fix = "an owner can raise the cap under Billing"
			}
		}
	}
	retry := time.Hour
	if !until.IsZero() {
		msg += " until " + until.UTC().Format("2006-01-02 15:04 UTC")
		if left := time.Until(until); left > 0 && left < retry {
			retry = left
		}
	}
	e := refuse(http.StatusTooManyRequests, gateway.TypeRateLimit, code, msg+": "+fix)
	e.RetryAfter = retry
	return e
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

// headerTags are the tags a client sent: at most 10, each one a key could
// carry (others are dropped).
func headerTags(v string) []string {
	var out []string
	for t := range strings.SplitSeq(v, ",") {
		if t = strings.TrimSpace(t); keysettings.ValidTag(t) && len(out) < 10 {
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
	route, err := pickRoute(c.Header.Get(brand.HeaderRoute), settings.Route, snap)
	if err != nil {
		return nil, err
	}
	m.Route = route.Name

	// A model router places the call: the one it names as its model
	// ("auto", "auto:fast"), else its route's.
	rt, pin, named := tiers.ParseModel(model, snap.Routers)
	if !named {
		rt, pin = snap.Routers[route.Router], ""
	}
	allowed := model
	if named {
		allowed = rt.Name
	}
	if !settings.Allows(allowed) {
		return nil, refuse(http.StatusForbidden, gateway.TypePermission, "model_not_allowed",
			"the API key may not call model "+model)
	}
	if rt != nil && crossFormat(c.Format) == 0 {
		if named {
			return nil, refuse(http.StatusBadRequest, gateway.TypeInvalidRequest, "router_format",
				"model routers serve Chat Completions and Messages calls, not "+c.Format.String())
		}
		rt = nil // a route's router places chat and messages calls only
	}
	if rt != nil && pin == "" {
		pin = strings.TrimSpace(c.Header.Get(brand.HeaderTier))
	}
	// The call's turn: its conversation's calls are known by it, whichever
	// replica serves them.
	routerName := ""
	if rt != nil {
		routerName = rt.Name
	}
	m.Turn, m.InTurn = conversation.KeysOf(conversation.Scope{Project: m.Project, APIKey: m.Key, User: m.User, Route: m.Route,
		Router: routerName, Family: c.Shape.Family}, conversation.Session(c.Header), c.Request.Turn)

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
	// The firewall reads the call before it goes anywhere, the decision
	// model included: nothing past it sees what it masks. A call it
	// blocks is placed without the decision model, for the policies that
	// may let it through to see where it would go.
	if e := snap.Firewall; e != nil {
		if err := r.inspect(e, c, m); err != nil {
			return nil, err
		}
	}
	format := c.Format
	var targets []gateway.Target
	var header http.Header
	switch {
	case rt != nil:
		if targets, header, err = r.place(ctx, snap, rt, pin, named, c, m); err != nil {
			return nil, err
		}
	default:
		targets = r.targets(snap, format, model, nil)
	}
	// An active rule that holds sends the call to its model first; the
	// model asked for stays the first fallback. Calls a router placed
	// follow its own rules.
	if rt == nil && len(snap.Rules) > 0 && r.cfg.Rules != nil {
		if rule, ok := rules.Match(r.cfg.Rules, snap.Rules, m.Route, RuleRequest(c, m)); ok {
			if routed := r.fallback(snap, format, rule.ToModel, nil); len(routed) > 0 {
				m.Rule = rule.Name
				for _, t := range targets {
					if !tried(routed, t.Provider, t.Model) {
						routed = append(routed, t)
					}
				}
				targets = routed
			}
		}
	}
	fallbacks := route.Fallbacks
	if o := m.Firewall; o != nil {
		var t firewall.Target
		if len(targets) > 0 {
			t = firewall.Target{Provider: targets[0].Provider, Kind: targets[0].Kind, Model: targets[0].Model}
		}
		snap.Firewall.Decide(o, RuleRequest(c, m), r.policyCall(ctx, snap, m, t))
		if o.Blocked {
			return nil, blocked(m, o)
		}
		// A policy's model serves the call alone: a fallback could be the
		// provider the policy keeps it from.
		if o.RouteTo != "" {
			if targets = r.fallback(snap, format, o.RouteTo, nil); len(targets) == 0 {
				e := refuse(http.StatusServiceUnavailable, gateway.TypeAPI, "firewall_no_provider",
					"the firewall's policy "+o.Policy+" sends the call to "+o.RouteTo+", which no provider of the project serves")
				e.Meta = m
				return nil, e
			}
			fallbacks = nil
		}
	}
	if len(targets) == 0 {
		return nil, refuse(http.StatusNotFound, gateway.TypeNotFound, "no_provider",
			"no provider of the project serves model "+model+" for "+format.String()+" calls: add one, or list the model on a provider")
	}
	for _, f := range fallbacks {
		targets = append(targets, r.fallback(snap, format, f, targets)...)
	}
	// A turn that fell back keeps the target that served its latest call
	// for the rest of the turn (an agent's tool loop): its prompt is cached
	// there, and its thinking was made there. The next human message starts
	// from the route's first model again. A router keeps its turn's target
	// within its tier (place).
	if rt == nil && len(targets) > 1 && m.InTurn && m.Turn.Continuation && r.cfg.Turns != nil &&
		(m.Firewall == nil || m.Firewall.RouteTo == "") {
		if rec, ok := r.cfg.Turns.Get(ctx, m.Project, m.Turn.Turn); ok {
			preferFirst(targets, rec.Provider, rec.Asked())
		}
	}
	// The plan's requests, counted once a call can go somewhere: hard on
	// Free, and on paid plans at the spend cap. If usage can't be counted,
	// the call goes through.
	if d, err := r.cfg.Quota.Use(ctx, m.Org, catalog.Requests, 1); err == nil && !d.Allowed() {
		return nil, r.outOfRequests(ctx, m.Org)
	}
	// An answer that gets tokenized values back isn't sampled: its judge
	// would read them. Nor is a call a policy decided: replays would take
	// it to the providers the policy chose against.
	if o := m.Firewall; o != nil && (o.Tokens != nil && o.Tokens.Len() > 0 || o.Policy != "") {
		m.Sampled = false
	}
	// Prompt caching applies while the plan includes it: the route keeps
	// the setting through a downgrade.
	promptCache := route.PromptCache && r.planHas(ctx, m.Org, catalog.PromptCaching)
	out := &gateway.Route{Targets: targets, Retries: route.Retries, PromptCache: promptCache, Header: header, Meta: m,
		Sample: m.Sampled}
	// OpenAI keeps a conversation's calls on the machines that cached them
	// when they share a key: a conversation that goes on gets its own.
	if promptCache && m.InTurn && (m.Turn.HumanTurns >= 2 || m.Turn.Continuation) {
		out.PromptKey = "pd-" + m.Turn.Conversation.String()
	}
	// OpenRouter keeps a session on one backend from its first call.
	if promptCache && m.InTurn {
		out.Session = "pd-" + m.Turn.Conversation.String()
	}
	// Each target's caching, by what its model's calls showed.
	if promptCache && r.cfg.Caching != nil {
		conversation := ""
		if m.InTurn {
			conversation = m.Turn.Conversation.String()
		}
		for i, t := range out.Targets {
			if !t.Pooled {
				continue
			}
			f := t.Format
			if f == 0 {
				f = format
			}
			out.Targets[i].Cache = r.cfg.Caching.Plan(t.Kind, markable(t.Kind, f.Anthropic(), t.Model), t.Model, t.Cache.Strategy,
				conversation, m.InTurn)
		}
	}
	// The family's learned lifetimes: an hour where its calls' gaps pay for
	// the dearer write.
	if t, ok := snap.CacheTTL[c.Shape.Family]; ok && promptCache {
		out.PromptTTL = gateway.PromptTTL{Prefix: t.Prefix, Tail: t.Tail}
	}
	if o := m.Firewall; o != nil {
		out.Body = o.Body
		if a := snap.Firewall.Response(o); a != nil {
			m.Answer, out.Response = a, a
		}
	}
	// The project keeps the call's payload: answers of the formats they
	// can be read in. A call the firewall tokenized isn't kept: those are
	// values the project chose to keep from leaving at all.
	if cp := snap.Capture; cp != nil && (m.Firewall == nil || m.Firewall.Tokens == nil || m.Firewall.Tokens.Len() == 0) &&
		r.cfg.Sample() < cp.SampleRate {
		out.Capture, m.Capture = true, cp
		out.CaptureAnswer = cp.Answers && (c.Format == wire.OpenAIChat || c.Format == wire.AnthropicMessages)
	}
	// Payload-free organizations cache no answers.
	if route.CacheTTL > 0 && !snap.PayloadFree {
		out.CacheTTL = time.Duration(route.CacheTTL) * time.Second
		out.CacheScope = m.Project.String() + ":" + route.Name
	}
	return out, nil
}

// inspect runs the firewall over a call's request. A request it masked
// replaces the client's for everything that reads it next; one it
// couldn't scan is refused or let through as its fail mode says.
func (r *Resolver) inspect(e *firewall.Engine, c *gateway.Call, m *Meta) error {
	o, err := e.Inspect(c.Body)
	m.Firewall = o
	if err != nil {
		slog.Warn("resolver: the firewall couldn't scan a request", "trace", c.Trace, "project", m.Project, "blocked", o.Blocked, "err", err)
		if o.Blocked {
			return blocked(m, o)
		}
		return nil
	}
	if o.Body != nil {
		req, err := wire.ParseRequest(c.Format, o.Body)
		if err != nil {
			// What the firewall wrote is JSON the gateway reads: never.
			o.Blocked, o.BlockedBy, o.Body = true, "scan", nil
			return blocked(m, o)
		}
		c.Body, c.Request = o.Body, req
	}
	return nil
}

// policyCall is what the firewall's policies know of a call going to t.
func (r *Resolver) policyCall(ctx context.Context, snap *snapshot.Snapshot, m *Meta, t firewall.Target) firewall.Call {
	var plan string
	if snap.Firewall.HasPolicies() && r.cfg.Entitlements != nil {
		if e, err := r.cfg.Entitlements(ctx, m.Org); err == nil {
			plan = e.Plan
		}
	}
	return firewall.Call{Target: t, Key: ids.Encode(ids.APIKey, m.Key), Plan: plan}
}

// blocked refuses a call the firewall blocked, the call's Meta with it
// for the request log.
func blocked(m *Meta, o *firewall.Outcome) *gateway.Error {
	e := refuse(http.StatusBadRequest, gateway.TypeInvalidRequest, "firewall_blocked", o.BlockMessage())
	e.Meta = m
	return e
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

// apiFormat is the providers' API format a call of format f is sent in
// without translation.
func apiFormat(f wire.Format) string {
	if f.Anthropic() {
		return providers.FormatAnthropic
	}
	return providers.FormatOpenAI
}

// crossFormat is the format a call of format f is translated to for a
// provider of the other API (0 when it can't be: only Chat Completions and
// Messages calls are).
func crossFormat(f wire.Format) wire.Format {
	switch f {
	case wire.OpenAIChat:
		return wire.AnthropicMessages
	case wire.AnthropicMessages:
		return wire.OpenAIChat
	}
	return 0
}

// target is a provider's target for a model and a call of format f, and
// whether the provider can take the call at all.
func (r *Resolver) target(p snapshot.Opened, f wire.Format, model string) (gateway.Target, bool) {
	// On a cloud account, the model's family picks the API, and the cloud
	// names the model its own way.
	format := providers.FormatFor(p.Kind, p.Format, model)
	t := gateway.Target{ID: p.ID.String() + ":" + model, Provider: p.Name, Kind: p.Kind, BaseURL: p.BaseURL,
		Model: providers.CloudModel(p.Kind, model), Header: p.Header, Auth: p.Auth}
	m, known := r.model(p.Kind, model)
	if known {
		// A model known not to reason has a client's effort taken out
		// when a call is moved to it.
		reasons, sure := m.Has(llm.FeatureReasoning)
		t.NoReasoning = sure && !reasons
		t.Reasoning = reasons
		t.Decisions = m.Decisions
	} else {
		// A model the price list doesn't have yet: what its docs say.
		m = llm.Known(model)
	}
	t.MinCache = int(m.MinCacheTokens)
	t.ThinkingOff, t.AlwaysThinks, t.NoSampling = m.ThinkingOff, m.AlwaysThinks, m.NoSampling
	// A project's own endpoint is marked only where its kind takes nothing
	// else: what it does with breakpoints is its own.
	t.Pooled = r.cfg.Pooled(p.Kind, p.BaseURL)
	t.Cache.Strategy = gateway.DefaultCacheStrategy(p.Kind, markable(p.Kind, format == providers.FormatAnthropic, t.Model),
		m.CacheWritePriced && t.Pooled)
	if format == apiFormat(f) {
		return t, true
	}
	t.Format = crossFormat(f)
	if t.Format == 0 {
		return t, false
	}
	// Translation passes a reasoning effort on to models that take one
	// (Reasoning), and lets Messages answers run as long as the model
	// writes.
	if known {
		t.MaxTokens = int(min(m.MaxOutput, maxTranslatedTokens))
	}
	return t, true
}

// WithEffort is t sent with a router tier's reasoning effort, fitted to
// its model: the nearest level the model takes (a tie going to the
// stronger), none for a model known not to reason or to take efforts, and
// the model's default, which the gateway needn't write.
func (r *Resolver) WithEffort(t gateway.Target, effort string) gateway.Target {
	if effort == "" || t.NoReasoning {
		return t
	}
	f := t.Format
	if f == 0 {
		f = wire.OpenAIChat
		if t.Kind == "anthropic" || providers.FormatFor(t.Kind, "", t.Model) == providers.FormatAnthropic {
			f = wire.AnthropicMessages
		}
	}
	m, known := r.model(t.Kind, t.Model)
	t.Effort = translate.FitEffort(effort, m.Efforts, known && m.Efforts != nil, f)
	t.DefaultEffort = m.DefaultEffort
	return t
}

// maxTranslatedTokens caps the answer of a Chat call that set no limit,
// sent to a Messages model: the model's own limit, at most this.
const maxTranslatedTokens = 32_768

// model is what the price list knows of a provider's model: its features
// and limits.
func (r *Resolver) model(kind, model string) (llm.Model, bool) {
	catalogName := ""
	if k, ok := providers.LookupKind(kind); ok {
		catalogName = k.Catalog
	}
	return r.cfg.Prices.Current().Lookup(catalogName, model)
}

// targets are the providers serving model for a call of format f, by
// priority: those speaking its API first, then those of the other API, the
// call translated, minus those already tried. A custom endpoint of the
// other API serves only the models it lists: without a list, nothing says
// it knows the model.
func (r *Resolver) targets(snap *snapshot.Snapshot, f wire.Format, model string, skip []gateway.Target) []gateway.Target {
	var same, cross []gateway.Target
	for _, p := range snap.Providers {
		if tried(skip, p.Name, model) || !r.serves(p.Provider, model) {
			continue
		}
		t, ok := r.target(p, f, model)
		switch {
		case !ok:
		case t.Format == 0:
			same = append(same, t)
		case p.Kind != providers.Custom || len(p.Models) > 0:
			cross = append(cross, t)
		}
	}
	return append(same, cross...)
}

// fallback resolves a route's fallback: provider/model when a provider of
// the project has that name, else a model any serving provider answers.
func (r *Resolver) fallback(snap *snapshot.Snapshot, f wire.Format, fb string, skip []gateway.Target) []gateway.Target {
	if name, model, ok := strings.Cut(fb, "/"); ok {
		for _, p := range snap.Providers {
			if p.Name != name || tried(skip, p.Name, model) {
				continue
			}
			if t, ok := r.target(p, f, model); ok {
				return []gateway.Target{t}
			}
		}
	}
	return r.targets(snap, f, fb, skip)
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
	if m, ok := r.cfg.Prices.Current().Lookup(kind.Catalog, model); ok && (m.Provider == kind.Catalog ||
		kind.Cloud && slices.Contains(kind.Vendors, m.Provider)) {
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
// kind), those of the client's API format first. Models of the other
// format answer Chat Completions and Messages calls, translated.
func (r *Resolver) Models(ctx context.Context, c *gateway.Call) ([]gateway.Model, error) {
	m, snap, err := r.caller(ctx, c)
	if err != nil {
		return nil, err
	}
	settings := snap.Keys[m.Key]
	served, _ := r.ServedModels(snap, apiFormat(c.Format))
	var out []gateway.Model
	for _, sm := range served {
		if settings.Allows(sm.ID) {
			out = append(out, gateway.Model{ID: sm.ID, Provider: sm.Providers[0].Name})
		}
	}
	return out, nil
}

// ServedModel is a model the project's providers serve, Providers in the
// order they are listed for it.
type ServedModel struct {
	ID        string
	Providers []snapshot.Provider
}

// ServedModels are the models a snapshot's providers serve: their own
// lists, else the catalog's models of their kind; the providers speaking
// format first, then by priority. Providers that take any model (a custom
// endpoint without a list, OpenRouter) come back in open: their models
// can't be listed.
func (r *Resolver) ServedModels(snap *snapshot.Snapshot, format string) (models []ServedModel, open []snapshot.Provider) {
	ordered := make([]snapshot.Provider, 0, len(snap.Providers))
	for _, p := range snap.Providers {
		if p.Format == format {
			ordered = append(ordered, p.Provider)
		}
	}
	for _, p := range snap.Providers {
		if p.Format != format {
			ordered = append(ordered, p.Provider)
		}
	}
	index := map[string]int{}
	add := func(id string, p snapshot.Provider) {
		if strings.HasSuffix(id, "*") {
			return
		}
		i, ok := index[id]
		if !ok {
			i = len(models)
			index[id] = i
			models = append(models, ServedModel{ID: id})
		}
		models[i].Providers = append(models[i].Providers, p)
	}
	for _, p := range ordered {
		if len(p.Models) > 0 {
			for _, id := range p.Models {
				add(id, p)
			}
			continue
		}
		kind, ok := providers.LookupKind(p.Kind)
		if p.Kind == providers.Custom || p.Kind == "openrouter" || !ok {
			open = append(open, p)
			continue
		}
		for _, cm := range r.cfg.Prices.Current().Models {
			if cm.Provider == kind.Catalog || kind.Cloud && slices.Contains(kind.Vendors, cm.Provider) {
				add(cm.ID, p)
			}
		}
	}
	return models, open
}

// markable reports a call in Anthropic's format whose model caches what a
// request marks: every Anthropic-format target but Bedrock's models
// through Converse that don't (Llama, Mistral, Cohere, …: only Claude and
// Nova take Converse's checkpoints), which are planned as caching on their
// own, unmarked.
func markable(kind string, anthropic bool, model string) bool {
	if !anthropic {
		return false
	}
	return kind != providers.Bedrock || !converse.For(model) || converse.CachingOf(model) != converse.CachesNothing
}
