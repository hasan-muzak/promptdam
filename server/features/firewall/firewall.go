// Package firewall keeps secrets and personal data a project's calls carry
// from reaching providers (project.md §6.4). Every text of a request is
// scanned (pkg/redact) before anything is sent: each finding's class
// (secret, pii or custom) says what happens to it, block, redact
// (mask), tokenize (a placeholder the answer gets the value back in),
// alert or off, and the project's CEL policies can then block, allow or
// send the call to another model. Answers are scanned too, for alerts, and
// get their tokens' values back, streamed or whole.
package firewall

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"

	"github.com/hasan-muzak/keel-core/pkg/policy"
	"github.com/hasan-muzak/keel-core/pkg/redact"

	"github.com/hasan-muzak/promptdam/server/features/rules"
)

// Classes of findings.
const (
	ClassSecret = "secret"
	ClassPII    = "pii"
	ClassCustom = "custom"
)

// Actions on a class's findings.
const (
	Off      = "off"
	Alert    = "alert"
	Redact   = "redact"
	Tokenize = "tokenize"
	Block    = "block"
)

// Policy actions.
const (
	PolicyBlock   = "block"
	PolicyAllow   = "allow"
	PolicyRouteTo = "route_to"
)

// Fail modes: what a call gets when the scan itself fails.
const (
	// FailSecretsClosed refuses the call when secrets are blocked, and
	// lets it through otherwise (the default, decided 2026-10-08).
	FailSecretsClosed = "secrets_closed"
	FailClosed        = "closed"
	FailOpen          = "open"
)

// PIIDetectors are personal data's detectors; every other default one
// finds secrets.
var PIIDetectors = []string{redact.Email, redact.Phone, redact.CreditCard, redact.IBAN, redact.USSSN, redact.TRTCKN}

// SecretDetectors are the default detectors that aren't personal data's.
var SecretDetectors = func() []string {
	var out []string
	for _, d := range redact.DefaultDetectors {
		if !slices.Contains(PIIDetectors, d) {
			out = append(out, d)
		}
	}
	return out
}()

// Settings are a project's firewall.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Secrets, PII and CustomAction are the classes' actions.
	Secrets string `json:"secrets"`
	PII     string `json:"pii"`
	// PIIDetectors are the personal data looked for (default all of
	// PIIDetectors; redact.IPv4 may be added).
	PIIDetectors []string `json:"pii_detectors,omitempty"`
	// Custom are the project's own patterns (name → regular expression).
	Custom       map[string]string `json:"custom,omitempty"`
	CustomAction string            `json:"custom_action,omitempty"`
	Policies     []Policy          `json:"policies,omitempty"`
	FailMode     string            `json:"fail_mode,omitempty"`
	// ScanResponses alerts on findings in answers.
	ScanResponses bool `json:"scan_responses"`
	// Excerpts keeps a masked excerpt around each finding in its event.
	Excerpts bool `json:"excerpts"`
}

// Defaults are a project's firewall until it sets one: secrets blocked,
// answers scanned.
func Defaults() Settings {
	return Settings{Enabled: true, Secrets: Block, PII: Off, CustomAction: Alert, FailMode: FailSecretsClosed, ScanResponses: true}
}

// Policy is a CEL condition and what it does to a call:
//
//	request    the call as routing rules see it (request.model, .tags…)
//	findings   map of detector → count of findings in the request
//	classes    map of class (secret, pii, custom) → count
//	locations  map of the request's part (messages, system, tools…) → count
//	target     map: provider, kind and model the call goes to
//	principal  map: key (key_…), developer and user of the call
//	org        map: plan, the organization's
type Policy struct {
	Name string `json:"name"`
	When string `json:"when"`
	// Action is block, allow (cancels a class's block) or route_to.
	Action string `json:"action"`
	// Model is route_to's model: model, or provider/model.
	Model string `json:"model,omitempty"`
}

// Limits.
const (
	MaxPolicies = 50
	MaxCustom   = 50
)

// InvalidError is settings that can't be saved; its message says why.
type InvalidError struct {
	Reason   string
	Problems []policy.Problem
	Policy   int
}

func (e *InvalidError) Error() string { return "firewall: " + e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidError{Reason: fmt.Sprintf(format, args...), Policy: -1}
}

// NewEnv is the environment policies are compiled in.
func NewEnv() (*policy.Env, error) {
	return policy.NewEnv(ext.NativeTypes(reflect.TypeFor[rules.Request](), ext.ParseStructTags(true)),
		cel.Variable("request", cel.ObjectType(reflect.TypeFor[rules.Request]().String())),
		cel.Variable("findings", cel.MapType(cel.StringType, cel.IntType)),
		cel.Variable("classes", cel.MapType(cel.StringType, cel.IntType)),
		cel.Variable("locations", cel.MapType(cel.StringType, cel.IntType)),
		cel.Variable("target", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("principal", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("org", cel.MapType(cel.StringType, cel.StringType)))
}

// Check validates settings and fills their defaults; policies are
// compiled in env.
func (s *Settings) Check(env *policy.Env) error {
	actions := []string{Off, Alert, Redact, Tokenize, Block}
	if s.Secrets == "" {
		s.Secrets = Block
	}
	if s.PII == "" {
		s.PII = Off
	}
	if s.CustomAction == "" {
		s.CustomAction = Alert
	}
	if s.FailMode == "" {
		s.FailMode = FailSecretsClosed
	}
	switch {
	case !slices.Contains(actions, s.Secrets), !slices.Contains(actions, s.PII), !slices.Contains(actions, s.CustomAction):
		return invalid("an action is off, alert, redact, tokenize or block")
	case s.Secrets == Tokenize:
		return invalid("secrets can't be tokenized: a model must never get them back to repeat")
	case !slices.Contains([]string{FailSecretsClosed, FailClosed, FailOpen}, s.FailMode):
		return invalid("fail_mode is secrets_closed, closed or open")
	case len(s.Custom) > MaxCustom:
		return invalid("at most %d custom patterns", MaxCustom)
	case len(s.Policies) > MaxPolicies:
		return invalid("at most %d policies", MaxPolicies)
	}
	for _, d := range s.PIIDetectors {
		if !slices.Contains(PIIDetectors, d) && d != redact.IPv4 {
			return invalid("%q is not a personal data detector (%s, %s)", d, strings.Join(PIIDetectors, ", "), redact.IPv4)
		}
	}
	for name := range s.Custom {
		if name == "" || len(name) > 32 || strings.ContainsAny(name, " ⟦⟧") || slices.Contains(redact.DefaultDetectors, name) {
			return invalid("%q: a custom pattern's name is 1 to 32 characters, unlike a built-in detector's", name)
		}
	}
	if _, err := redact.New(redact.Options{Detectors: []string{}, Patterns: s.Custom}); err != nil {
		return invalid("a custom pattern doesn't compile: %v", err)
	}
	for i := range s.Policies {
		p := &s.Policies[i]
		p.Name, p.Model = strings.TrimSpace(p.Name), strings.TrimSpace(p.Model)
		switch {
		case p.Name == "" || len(p.Name) > 100:
			return invalid("policy %d: a name is 1 to 100 characters", i+1)
		case p.Action != PolicyBlock && p.Action != PolicyAllow && p.Action != PolicyRouteTo:
			return invalid("policy %s: the action is block, allow or route_to", p.Name)
		case p.Action == PolicyRouteTo && (p.Model == "" || len(p.Model) > 300):
			return invalid("policy %s: route_to needs a model, or provider/model", p.Name)
		}
		if _, err := env.Compile(p.When); err != nil {
			var ce *policy.CompileError
			if errors.As(err, &ce) {
				return &InvalidError{Reason: fmt.Sprintf("policy %s doesn't compile: %s", p.Name, ce.Error()), Problems: ce.Problems, Policy: i}
			}
			return err
		}
	}
	return nil
}

// Engine applies a project's settings to its calls. It is safe for
// concurrent use.
type Engine struct {
	s     Settings
	r     *redact.Redactor
	class map[string]string // detector → class
	env   *policy.Env
}

// Compile builds an Engine from checked settings; nil when the firewall
// is off.
func Compile(s Settings, env *policy.Env) (*Engine, error) {
	if !s.Enabled {
		return nil, nil
	}
	e := &Engine{s: s, class: map[string]string{}, env: env}
	detectors := []string{} // none is none, not redact's defaults
	if s.Secrets != Off {
		for _, d := range SecretDetectors {
			detectors = append(detectors, d)
			e.class[d] = ClassSecret
		}
	}
	if s.PII != Off {
		pii := s.PIIDetectors
		if len(pii) == 0 {
			pii = PIIDetectors
		}
		for _, d := range pii {
			detectors = append(detectors, d)
			e.class[d] = ClassPII
		}
	}
	custom := map[string]string{}
	if s.CustomAction != Off {
		custom = s.Custom
		for name := range custom {
			e.class[name] = ClassCustom
		}
	}
	if len(detectors) == 0 && len(custom) == 0 && len(s.Policies) == 0 {
		return nil, nil
	}
	r, err := redact.New(redact.Options{Detectors: detectors, Patterns: custom})
	if err != nil {
		return nil, fmt.Errorf("firewall: %w", err)
	}
	e.r = r
	return e, nil
}

// Settings are the engine's settings.
func (e *Engine) Settings() Settings { return e.s }

func (e *Engine) action(class string) string {
	switch class {
	case ClassSecret:
		return e.s.Secrets
	case ClassPII:
		return e.s.PII
	}
	return e.s.CustomAction
}

// Hit is a detector's findings in one part of a call.
type Hit struct {
	Detector, Class, Action string
	// Location is the part: the request's top-level field (messages,
	// system, tools…) or "response".
	Location string
	Count    int
	// Excerpt is the text around the first finding, every finding in it
	// masked (only when the settings keep excerpts).
	Excerpt string
}

// Outcome is what the firewall made of a request.
type Outcome struct {
	Hits []Hit
	// Body is the request with findings masked or tokenized (nil when
	// unchanged); Tokens restores the tokenized ones in the answer.
	Body   []byte
	Tokens *redact.Tokenizer
	// Blocked refuses the call; BlockedBy is the class or the policy that
	// blocked it.
	Blocked   bool
	BlockedBy string
	// Policy is the policy that decided, RouteTo its model (route_to).
	Policy, RouteTo string
	// Failed reports a scan that failed and was let through (fail open).
	Failed bool
}

// Kinds are the detectors found, sorted.
func (o *Outcome) Kinds() []string {
	seen := map[string]bool{}
	for _, h := range o.Hits {
		seen[h.Detector] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// Action is the strongest action taken on the request: block, tokenize,
// redact, alert, or "" when nothing was found.
func (o *Outcome) Action() string {
	if o.Blocked {
		return Block
	}
	rank := map[string]int{Alert: 1, Redact: 2, Tokenize: 3}
	best := ""
	for _, h := range o.Hits {
		if rank[h.Action] > rank[best] {
			best = h.Action
		}
	}
	return best
}

// skipped are keys whose strings hold identifiers, not text.
var skipped = map[string]bool{
	"model": true, "role": true, "type": true, "id": true, "tool_call_id": true, "tool_use_id": true, "call_id": true,
	"media_type": true, "signature": true, "encrypted_content": true, "previous_response_id": true, "service_tier": true,
	"reasoning_effort": true, "effort": true, "detail": true, "format": true, "object": true, "finish_reason": true,
	"stop_reason": true,
}

// blob reports a string that holds data, not text: a data URL or a long
// run of base64.
func blob(s string) bool {
	if strings.HasPrefix(s, "data:") && strings.Contains(s[:min(len(s), 100)], ";base64,") {
		return true
	}
	if len(s) < 1024 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ErrScan is a request the firewall couldn't read.
var ErrScan = errors.New("firewall: the request couldn't be scanned")

// Inspect scans a request body and applies the classes' actions: the
// policies come after, with Decide. A scan that fails returns ErrScan
// with what fail mode says: a blocked outcome, or one let through.
func (e *Engine) Inspect(body []byte) (*Outcome, error) {
	o := &Outcome{}
	hits := map[[2]string]*Hit{}
	var order [][2]string
	out, changed, err := rewrite(body, func(w *Walker, s string) (string, bool) {
		if skipped[w.Key()] || blob(s) {
			return "", false
		}
		fs := e.r.Find(s)
		if len(fs) == 0 {
			return "", false
		}
		loc := w.Top()
		var b strings.Builder
		last, changed := 0, false
		for _, f := range fs {
			class := e.class[f.Detector]
			action := e.action(class)
			value := s[f.Start:f.End]
			// A value that JSON must escape can't be put back verbatim in
			// a streamed answer's text: it is masked instead.
			if action == Tokenize && !plain(value) {
				action = Redact
			}
			k := [2]string{f.Detector, loc}
			h := hits[k]
			if h == nil {
				h = &Hit{Detector: f.Detector, Class: class, Action: action, Location: loc}
				if e.s.Excerpts {
					h.Excerpt = e.excerpt(s, f)
				}
				hits[k] = h
				order = append(order, k)
			}
			h.Count++
			b.WriteString(s[last:f.Start])
			switch action {
			case Block:
				o.Blocked, o.BlockedBy = true, class
				b.WriteString(value)
			case Redact:
				b.WriteString("[REDACTED:" + f.Detector + "]")
				changed = true
			case Tokenize:
				if o.Tokens == nil {
					o.Tokens = e.r.Tokenizer()
				}
				b.WriteString(o.Tokens.Token(f.Detector, value))
				changed = true
			default:
				b.WriteString(value)
			}
			last = f.End
		}
		if !changed {
			return "", false
		}
		b.WriteString(s[last:])
		return b.String(), true
	})
	for _, k := range order {
		o.Hits = append(o.Hits, *hits[k])
	}
	if err != nil {
		o.Hits = nil
		if e.s.FailMode == FailClosed || e.s.FailMode == FailSecretsClosed && e.s.Secrets == Block {
			o.Blocked, o.BlockedBy = true, "scan"
		} else {
			o.Failed = true
		}
		return o, ErrScan
	}
	if changed {
		o.Body = out
	}
	return o, nil
}

// plain reports a value JSON writes as it is.
func plain(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// excerpt is up to 40 bytes either side of a finding, every finding in it
// masked.
func (e *Engine) excerpt(s string, f redact.Finding) string {
	start, end := max(0, f.Start-40), min(len(s), f.End+40)
	for start > 0 && start < len(s) && s[start]&0xC0 == 0x80 {
		start--
	}
	for end < len(s) && s[end]&0xC0 == 0x80 {
		end++
	}
	masked, _ := e.r.Mask(s[start:end])
	return masked
}

// Target is where a call goes, for policies.
type Target struct {
	Provider, Kind, Model string
}

// Call is what policies know of a call beside its request.
type Call struct {
	Target Target
	// Key is the call's API key (key_…), Plan its organization's plan.
	Key, Plan string
}

// HasPolicies reports whether Decide has anything to run.
func (e *Engine) HasPolicies() bool { return len(e.s.Policies) > 0 }

// Decide runs the policies on a call the scan let through or blocked:
// the first that holds blocks it, allows it (cancelling a class's block)
// or sends it to another model. A policy that fails to evaluate doesn't
// hold; a request that couldn't be scanned stays as fail mode left it.
func (e *Engine) Decide(o *Outcome, req rules.Request, c Call) {
	if len(e.s.Policies) == 0 || o.BlockedBy == "scan" || o.Failed {
		return
	}
	findings, classes := map[string]int64{}, map[string]int64{ClassSecret: 0, ClassPII: 0, ClassCustom: 0}
	locations := map[string]int64{}
	for d := range e.class {
		findings[d] = 0
	}
	for _, h := range o.Hits {
		findings[h.Detector] += int64(h.Count)
		classes[h.Class] += int64(h.Count)
		locations[h.Location] += int64(h.Count)
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}
	t := c.Target
	vars := map[string]any{"request": req, "findings": findings, "classes": classes, "locations": locations,
		"target":    map[string]string{"provider": t.Provider, "kind": t.Kind, "model": t.Model},
		"principal": map[string]string{"key": c.Key, "developer": req.Developer, "user": req.User},
		"org":       map[string]string{"plan": c.Plan}}
	for _, p := range e.s.Policies {
		prg, err := e.env.Compile(p.When)
		if err != nil {
			continue
		}
		if ok, err := prg.Bool(vars); err != nil || !ok {
			continue
		}
		o.Policy = p.Name
		switch p.Action {
		case PolicyBlock:
			o.Blocked, o.BlockedBy = true, "policy:"+p.Name
		case PolicyAllow:
			o.Blocked, o.BlockedBy = false, ""
		case PolicyRouteTo:
			o.RouteTo = p.Model
		}
		return
	}
}

// BlockMessage says why a call was refused, naming what was found, never
// the values.
func (o *Outcome) BlockMessage() string {
	switch {
	case strings.HasPrefix(o.BlockedBy, "policy:"):
		return "the firewall's policy " + strings.TrimPrefix(o.BlockedBy, "policy:") + " refused the request"
	case o.BlockedBy == "scan":
		return "the firewall couldn't scan the request, and refuses what it can't scan"
	}
	var kinds []string
	for _, h := range o.Hits {
		if h.Action == Block {
			kinds = append(kinds, h.Detector)
		}
	}
	sort.Strings(kinds)
	return "the request holds " + strings.Join(slices.Compact(kinds), ", ") + ", which the firewall blocks"
}
