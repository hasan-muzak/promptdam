package handlers

import (
	"encoding/json"
	"errors"
	"fmt"

	muzak "muzak.dev/framework"

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
	When   string `json:"when" doc:"CEL over request (as routing rules see it), findings (detector → count), classes (secret, pii, custom → count) and target (provider, kind, model)"`
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
		e.Decide(o, resolver.RuleRequest(call, meta), t)
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
