package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

// CodeInvalidFirewall is settings the firewall can't apply.
const CodeInvalidFirewall = "invalid_firewall"

// FirewallPolicy is a CEL condition and what it does to a call.
type FirewallPolicy struct {
	Name   string `json:"name"`
	When   string `json:"when" doc:"CEL over request (as routing rules see it), findings (detector → count), classes (secret, pii, custom → count), locations (messages, system, tools… → count), target (provider, kind, model), principal (key, developer, user) and org (plan)"`
	Action string `json:"action" doc:"block, allow (cancels a class's block) or route_to"`
	Model  string `json:"model,omitempty" doc:"route_to's model: model, or provider/model. The call goes there only, without fallbacks"`
}

// FirewallSettings are a project's firewall.
type FirewallSettings struct {
	Enabled bool   `json:"enabled" doc:"Off, calls go through unscanned"`
	Secrets string `json:"secrets" doc:"What API keys, tokens, private keys and connection strings get: block (the default), redact, alert or off"`
	PII     string `json:"pii" doc:"What personal data gets: off (the default), alert, redact, tokenize (the model sees a placeholder; the answer gets the value back) or block. Redact and tokenize need a plan with PII redaction"`
	// PIIDetectors are the personal data looked for.
	PIIDetectors  []string          `json:"pii_detectors" doc:"email, phone, credit_card, iban, us_ssn, tr_tckn, ipv4; empty: all but ipv4"`
	Custom        map[string]string `json:"custom" doc:"The project's own patterns: name → regular expression (RE2). Needs a plan with custom policies"`
	CustomAction  string            `json:"custom_action" doc:"What custom patterns' findings get: alert (the default), redact, tokenize, block or off"`
	Policies      []FirewallPolicy  `json:"policies" doc:"In order: the first that holds decides. Needs a plan with firewall policies"`
	FailMode      string            `json:"fail_mode" doc:"When a request can't be scanned: secrets_closed (refused while secrets are blocked; the default), closed or open"`
	ScanResponses bool              `json:"scan_responses" doc:"Answers are scanned too, for alerts: what was written can't be taken back"`
	Excerpts      bool              `json:"excerpts" doc:"Keep the text around each finding, findings masked, in its event"`
}

// Validate implements muzak.Validatable.
func (s *FirewallSettings) Validate(v *muzak.Validation) {
	v.Slice(&s.Policies).MaxItems(firewall.MaxPolicies)
	v.Slice(&s.PIIDetectors).MaxItems(10)
}

func (s FirewallSettings) settings() firewall.Settings {
	out := firewall.Settings{Enabled: s.Enabled, Secrets: s.Secrets, PII: s.PII, PIIDetectors: s.PIIDetectors, Custom: s.Custom,
		CustomAction: s.CustomAction, FailMode: s.FailMode, ScanResponses: s.ScanResponses, Excerpts: s.Excerpts}
	for _, p := range s.Policies {
		out.Policies = append(out.Policies, firewall.Policy(p))
	}
	return out
}

func firewallOut(s firewall.Settings) FirewallSettings {
	out := FirewallSettings{Enabled: s.Enabled, Secrets: s.Secrets, PII: s.PII, PIIDetectors: s.PIIDetectors, Custom: s.Custom,
		CustomAction: s.CustomAction, Policies: []FirewallPolicy{}, FailMode: s.FailMode, ScanResponses: s.ScanResponses,
		Excerpts: s.Excerpts}
	if out.PIIDetectors == nil {
		out.PIIDetectors = []string{}
	}
	if out.Custom == nil {
		out.Custom = map[string]string{}
	}
	for _, p := range s.Policies {
		out.Policies = append(out.Policies, FirewallPolicy(p))
	}
	return out
}

// PutFirewallIn replaces a project's firewall.
type PutFirewallIn struct {
	platform.ProjectPath
	FirewallSettings
}

// GetFirewall returns a project's firewall.
func (h *Handlers) GetFirewall(ctx *muzak.Context, _ platform.ProjectPath) (FirewallSettings, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	s, err := h.Firewall.Get(ctx.Context(), pa.Project.ID)
	if err != nil {
		return FirewallSettings{}, err
	}
	return firewallOut(s), nil
}

// PutFirewall replaces a project's firewall.
func (h *Handlers) PutFirewall(ctx *muzak.Context, in PutFirewallIn) (FirewallSettings, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	old, err := h.Firewall.Get(ctx.Context(), pa.Project.ID)
	if err != nil {
		return FirewallSettings{}, err
	}
	s := in.settings()
	if need := s.Needs(old); len(need) > 0 {
		if _, err := platform.RequireFeature(h.RouterDeps.Billing, h.RouterDeps.AppURL, need...)(ctx); err != nil {
			return FirewallSettings{}, err
		}
	}
	saved, err := h.Firewall.Put(ctx.Context(), pa.OrgID(), pa.Project.ID, s)
	if err != nil {
		return FirewallSettings{}, firewallFail(err)
	}
	return firewallOut(saved), nil
}

// TestFirewallIn is a request to try the firewall on.
type TestFirewallIn struct {
	platform.ProjectPath
	APIFormat string          `json:"api_format" doc:"openai.chat (the default), openai.responses, openai.embeddings or anthropic.messages"`
	Request   json.RawMessage `json:"request" doc:"A request body, as a client would send it"`
	// Settings try settings before saving them.
	Settings  *FirewallSettings `json:"settings,omitempty" doc:"Settings to try instead of the saved ones; no plan needed"`
	Key       string            `json:"key" doc:"For policies: the call's API key (key_…)"`
	Route     string            `json:"route" doc:"For policies: the call's route"`
	User      string            `json:"user"`
	Developer string            `json:"developer"`
	Tags      []string          `json:"tags"`
}

// Validate implements muzak.Validatable.
func (in *TestFirewallIn) Validate(v *muzak.Validation) {
	v.Slice(&in.Tags).MaxItems(20)
}

// FirewallHitOut is a detector's findings in one part of a call.
type FirewallHitOut struct {
	Detector string `json:"detector"`
	Class    string `json:"class" doc:"secret, pii or custom"`
	Action   string `json:"action" doc:"What the findings got: block, redact, tokenize or alert"`
	Location string `json:"location" doc:"The request's top-level field they are in: messages, system, tools, input…"`
	Count    int    `json:"count"`
	Excerpt  string `json:"excerpt,omitempty" doc:"The text around the first one, findings masked (with excerpts on)"`
}

// TestFirewallOut is what the firewall would do with the request.
type TestFirewallOut struct {
	Action    string           `json:"action" doc:"The strongest action taken: block, tokenize, redact, alert, or empty when nothing was found"`
	Blocked   bool             `json:"blocked"`
	BlockedBy string           `json:"blocked_by,omitempty" doc:"The class (secret, pii, custom), policy:<name>, or scan"`
	Message   string           `json:"message,omitempty" doc:"What a blocked client is told"`
	Policy    string           `json:"policy,omitempty" doc:"The policy that decided"`
	RouteTo   string           `json:"route_to,omitempty" doc:"The model a route_to policy sends the call to"`
	Hits      []FirewallHitOut `json:"hits"`
	// Request is what the provider would get.
	Request json.RawMessage `json:"request" doc:"The request as the provider would get it, findings masked or tokenized; null when unchanged"`
	Target  struct {
		Provider string `json:"provider"`
		Kind     string `json:"kind"`
		Model    string `json:"model"`
	} `json:"target" doc:"Where the call would go, for policies: the model asked for at the project's first provider serving it"`
}

// TestFirewall runs the firewall on a request, without calling any
// provider: what it finds, what the provider would get, the policy that
// decides.
func (h *Handlers) TestFirewall(ctx *muzak.Context, in TestFirewallIn) (TestFirewallOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	format, ok := map[string]wire.Format{"": wire.OpenAIChat, wire.OpenAIChat.String(): wire.OpenAIChat,
		wire.OpenAIResponses.String(): wire.OpenAIResponses, wire.OpenAIEmbeddings.String(): wire.OpenAIEmbeddings,
		wire.AnthropicMessages.String(): wire.AnthropicMessages}[in.APIFormat]
	if !ok {
		return TestFirewallOut{}, muzak.UnprocessableEntity("api_format is openai.chat, openai.responses, openai.embeddings or " +
			"anthropic.messages").WithCode(CodeInvalidFirewall)
	}
	req, err := wire.ParseRequest(format, in.Request)
	if err != nil {
		return TestFirewallOut{}, muzak.UnprocessableEntity("request is not a " + format.String() + " request body: " + err.Error()).
			WithCode(CodeInvalidFirewall)
	}
	var s firewall.Settings
	if in.Settings != nil {
		s = in.Settings.settings()
		if err := s.Check(firewall.Env()); err != nil {
			return TestFirewallOut{}, firewallFail(err)
		}
	} else if s, err = h.Firewall.Get(ctx.Context(), pa.Project.ID); err != nil {
		return TestFirewallOut{}, err
	}
	out := TestFirewallOut{Hits: []FirewallHitOut{}}
	e := firewall.Load(s)
	if e == nil {
		return out, nil
	}
	o, err := e.Inspect(in.Request)
	if err != nil && !errors.Is(err, firewall.ErrScan) {
		return TestFirewallOut{}, err
	}
	if len(s.Policies) > 0 {
		d, err := h.RouterDeps.Sources.Build(ctx.Context(), pa.OrgID(), pa.Project.ID)
		if err != nil {
			return TestFirewallOut{}, err
		}
		snap := snapshot.Plain(pa.Project.ID, d)
		var t firewall.Target
		if ts := h.RouterDeps.Explainer.Targets(snap, format, req.Model); len(ts) > 0 {
			t = firewall.Target{Provider: ts[0].Provider, Kind: ts[0].Kind, Model: ts[0].Model}
		}
		call := &gateway.Call{Format: format, Request: req, Shape: families.Of(req.Prefix, ""), BodyBytes: len(in.Request), Body: in.Request}
		meta := &resolver.Meta{Route: in.Route, User: in.User, Developer: in.Developer, Tags: in.Tags}
		var plan string
		if ent, err := h.RouterDeps.Billing.Entitlements(ctx.Context(), pa.OrgID()); err == nil {
			plan = ent.Plan
		}
		e.Decide(o, resolver.RuleRequest(call, meta), firewall.Call{Target: t, Key: in.Key, Plan: plan})
		out.Target.Provider, out.Target.Kind, out.Target.Model = t.Provider, t.Kind, t.Model
	}
	out.Action, out.Blocked, out.BlockedBy, out.Policy, out.RouteTo = o.Action(), o.Blocked, o.BlockedBy, o.Policy, o.RouteTo
	if o.Blocked {
		out.Message = o.BlockMessage()
	}
	if o.Body != nil && !o.Blocked {
		out.Request = o.Body
	}
	for _, hit := range o.Hits {
		out.Hits = append(out.Hits, FirewallHitOut(hit))
	}
	return out, nil
}

func firewallFail(err error) error {
	var bad *firewall.InvalidError
	if !errors.As(err, &bad) {
		return err
	}
	e := muzak.UnprocessableEntity(bad.Reason).WithCode(CodeInvalidFirewall)
	if len(bad.Problems) > 0 {
		ds := problems(bad.Problems)
		for i := range ds {
			ds[i].Field = fmt.Sprintf("policies[%d].when", bad.Policy)
		}
		e = e.WithDetails(ds...)
	}
	return e
}

// ListFirewallEventsIn filters a project's firewall events.
type ListFirewallEventsIn struct {
	platform.ProjectPath
	httpx.Page
	TimeRange
	Detector string `query:"detector" doc:"Only this detector's: aws_access_key, email, a custom pattern's name…"`
	Class    string `query:"class" doc:"secret, pii or custom"`
	Action   string `query:"action" doc:"block, tokenize, redact or alert (or allow, route_to: a policy's event)"`
	Location string `query:"location" doc:"messages, system, tools, input…, or response"`
	Key      string `query:"key" doc:"Only this API key's calls (key_…)"`
	Request  string `query:"request" doc:"Only this call's events: its trace (req_…)"`
	Blocked  bool   `query:"blocked" doc:"Only the events of calls the firewall refused"`
}

// FirewallEventOut is what the firewall found in one part of a call, or
// the policy that decided.
type FirewallEventOut struct {
	Request        string    `json:"request" doc:"The call's trace (req_…), as in the request log"`
	Time           time.Time `json:"time"`
	Key            string    `json:"key" doc:"API key ID (key_…)"`
	Developer      string    `json:"developer"`
	Route          string    `json:"route"`
	RequestedModel string    `json:"requested_model"`
	Provider       string    `json:"provider" doc:"The provider that answered; empty when the call was refused"`
	Detector       string    `json:"detector" doc:"Empty for a policy's event"`
	Class          string    `json:"class"`
	Action         string    `json:"action"`
	Location       string    `json:"location"`
	Count          int       `json:"count"`
	Blocked        bool      `json:"blocked" doc:"The call was refused"`
	BlockedBy      string    `json:"blocked_by" doc:"The class (secret, pii, custom), policy:<name>, or scan"`
	Policy         string    `json:"policy" doc:"The policy that decided the call"`
	Excerpt        string    `json:"excerpt" doc:"The text around the finding, findings masked; only with excerpts on"`
}

// ListFirewallEvents lists a project's firewall events, newest first.
func (h *Handlers) ListFirewallEvents(ctx *muzak.Context, in ListFirewallEventsIn) (httpx.List[FirewallEventOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := in.bounds(7 * 24 * time.Hour)
	if err != nil {
		return httpx.List[FirewallEventOut]{}, err
	}
	f := firewall.EventFilter{From: from, To: to, Detector: in.Detector, Class: in.Class, Action: in.Action, Location: in.Location,
		Trace: in.Request, Blocked: in.Blocked}
	if in.Key != "" {
		if f.Key, err = ids.Decode(ids.APIKey, in.Key); err != nil {
			return httpx.List[FirewallEventOut]{}, httpx.NotFound("API key")
		}
	}
	var after *firewall.EventCursor
	if in.Cursor != "" {
		c, err := decodeEventCursor(in.Cursor)
		if err != nil {
			return httpx.List[FirewallEventOut]{}, firewallQueryFail(err)
		}
		after = &c
	}
	rows, next, err := firewall.ListEvents(ctx.Context(), h.RouterDeps.Events, pa.Project.ID, f, after, in.Limit)
	if err != nil {
		return httpx.List[FirewallEventOut]{}, firewallQueryFail(err)
	}
	out := httpx.List[FirewallEventOut]{Data: make([]FirewallEventOut, len(rows))}
	for i, e := range rows {
		out.Data[i] = FirewallEventOut{Request: e.Trace, Time: e.TS.UTC(), Key: ids.Encode(ids.APIKey, e.KeyID), Developer: e.Developer,
			Route: e.Route, RequestedModel: e.RequestedModel, Provider: e.Provider, Detector: e.Detector, Class: e.Class,
			Action: e.Action, Location: e.Location, Count: int(e.Count), Blocked: e.Blocked, BlockedBy: e.BlockedBy,
			Policy: e.Policy, Excerpt: e.Excerpt}
	}
	if next != nil {
		out.NextCursor = encodeEventCursor(*next)
	}
	return out, nil
}

// encodeEventCursor writes a cursor as an opaque string.
func encodeEventCursor(c firewall.EventCursor) string {
	raw, _ := json.Marshal([]any{c.TS.UnixMilli(), c.Trace, c.Detector, c.Location})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeEventCursor(s string) (firewall.EventCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	var parts []json.RawMessage
	if err != nil || json.Unmarshal(raw, &parts) != nil || len(parts) != 4 {
		return firewall.EventCursor{}, firewall.ErrCursor
	}
	var ms int64
	var c firewall.EventCursor
	if json.Unmarshal(parts[0], &ms) != nil || json.Unmarshal(parts[1], &c.Trace) != nil ||
		json.Unmarshal(parts[2], &c.Detector) != nil || json.Unmarshal(parts[3], &c.Location) != nil {
		return firewall.EventCursor{}, firewall.ErrCursor
	}
	c.TS = time.UnixMilli(ms).UTC()
	return c, nil
}

// FirewallStatsIn asks for a project's firewall stats.
type FirewallStatsIn struct {
	platform.ProjectPath
	TimeRange
}

// FirewallStatsOut is what the firewall did over a time range.
type FirewallStatsOut struct {
	From      time.Time             `json:"from"`
	To        time.Time             `json:"to"`
	Calls     uint64                `json:"calls" doc:"Calls the firewall found something in, or a policy decided"`
	Blocked   uint64                `json:"blocked" doc:"Calls it refused"`
	Actions   []FirewallActionOut   `json:"actions"`
	Detectors []FirewallDetectorOut `json:"detectors" doc:"The 50 with the most findings"`
	Days      []FirewallDayOut      `json:"days" doc:"Per UTC day with events"`
}

// FirewallActionOut is the calls and findings an action took.
type FirewallActionOut struct {
	Action   string `json:"action"`
	Calls    uint64 `json:"calls"`
	Findings uint64 `json:"findings"`
}

// FirewallDetectorOut is a detector's calls and findings.
type FirewallDetectorOut struct {
	Detector string `json:"detector"`
	Class    string `json:"class"`
	Calls    uint64 `json:"calls"`
	Findings uint64 `json:"findings"`
}

// FirewallDayOut is a day's calls and blocked calls.
type FirewallDayOut struct {
	Day     time.Time `json:"day"`
	Calls   uint64    `json:"calls"`
	Blocked uint64    `json:"blocked"`
}

// GetFirewallStats returns what the firewall did over a time range.
func (h *Handlers) GetFirewallStats(ctx *muzak.Context, in FirewallStatsIn) (FirewallStatsOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := in.bounds(7 * 24 * time.Hour)
	if err != nil {
		return FirewallStatsOut{}, err
	}
	s, err := firewall.ReadStats(ctx.Context(), h.RouterDeps.Events, pa.Project.ID, from, to)
	if err != nil {
		return FirewallStatsOut{}, firewallQueryFail(err)
	}
	out := FirewallStatsOut{From: from, To: to, Calls: s.Calls, Blocked: s.Blocked, Actions: make([]FirewallActionOut, len(s.Actions)),
		Detectors: make([]FirewallDetectorOut, len(s.Detectors)), Days: make([]FirewallDayOut, len(s.Days))}
	for i, a := range s.Actions {
		out.Actions[i] = FirewallActionOut(a)
	}
	for i, d := range s.Detectors {
		out.Detectors[i] = FirewallDetectorOut(d)
	}
	for i, d := range s.Days {
		out.Days[i] = FirewallDayOut{Day: d.Day.UTC(), Calls: d.Calls, Blocked: d.Blocked}
	}
	return out, nil
}

func firewallQueryFail(err error) error {
	switch {
	case errors.Is(err, firewall.ErrRange):
		return muzak.UnprocessableEntity("the time range must start before it ends and cover at most 31 days of events, " +
			"or 90 days of stats").WithCode(CodeInvalidQuery)
	case errors.Is(err, firewall.ErrCursor):
		return muzak.UnprocessableEntity("the cursor is invalid").WithCode(CodeInvalidQuery)
	}
	return err
}

// FirewallPresetsOut lists the firewall's presets.
type FirewallPresetsOut struct {
	Data []firewall.Preset `json:"data"`
}

// FirewallPresets lists the firewall's presets: each class's detectors.
func (h *Handlers) FirewallPresets(*muzak.Context, muzak.Empty) (FirewallPresetsOut, error) {
	return FirewallPresetsOut{Data: firewall.Presets}, nil
}
