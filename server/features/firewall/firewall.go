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
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
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
// The countries' national IDs are among them (core redact's opt-in
// NationalIDs), so a project masking personal data masks those too.
var PIIDetectors = append([]string{redact.Email, redact.Phone, redact.CreditCard, redact.IBAN, redact.USSSN, redact.TRTCKN},
	redact.NationalIDs...)

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
	// Secrets, PII, Sensitive and CustomAction are the classes' actions.
	Secrets string `json:"secrets"`
	PII     string `json:"pii"`
	// Sensitive is the special categories' (ClassSensitive), which only the
	// model finds.
	Sensitive string `json:"sensitive,omitempty"`
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
	// Model is what the detector model reads for (round PD).
	Model ModelSettings `json:"model"`
}

// Defaults are a project's firewall until it sets one: secrets blocked,
// answers scanned.
func Defaults() Settings {
	return Settings{Enabled: true, Secrets: Block, PII: Off, Sensitive: Alert, CustomAction: Alert, FailMode: FailSecretsClosed,
		ScanResponses: true, Model: ModelSettings{FailMode: ModelFailOpen}}
}

// Policy is a CEL condition and what it does to a call:
//
//	request    the call as routing rules see it (request.model, .tags…)
//	findings   map of detector → count of findings in the request
//	classes    map of class (secret, pii, sensitive, custom) → count
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
	// MaxPattern bounds a custom pattern, MaxCondition a policy's
	// condition: every gateway compiles them on each reload.
	MaxPattern   = 1000
	MaxCondition = 4096
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
	return policy.NewEnv(rules.Types(),
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
	if s.Sensitive == "" {
		s.Sensitive = Alert
	}
	if s.FailMode == "" {
		s.FailMode = FailSecretsClosed
	}
	switch {
	case !slices.Contains(actions, s.Secrets), !slices.Contains(actions, s.PII), !slices.Contains(actions, s.CustomAction),
		!slices.Contains(actions, s.Sensitive):
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
	if err := s.checkModel(); err != nil {
		return err
	}
	for _, d := range s.PIIDetectors {
		if !slices.Contains(PIIDetectors, d) && d != redact.IPv4 {
			return invalid("%q is not a personal data detector (%s, %s)", d, strings.Join(PIIDetectors, ", "), redact.IPv4)
		}
	}
	for name, re := range s.Custom {
		if _, model := modelLabel(name); name == "" || len(name) > 32 || strings.ContainsAny(name, " ⟦⟧") ||
			slices.Contains(redact.DefaultDetectors, name) || slices.Contains(redact.NationalIDs, name) || model {
			return invalid("%q: a custom pattern's name is 1 to 32 characters, unlike a built-in detector's", name)
		}
		if re == "" || len(re) > MaxPattern {
			return invalid("pattern %s: 1 to %d characters", name, MaxPattern)
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
		case strings.TrimSpace(p.When) == "" || len(p.When) > MaxCondition:
			return invalid("policy %s: a condition is 1 to %d characters", p.Name, MaxCondition)
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
	// all finds everything, whatever the classes' actions, to mask
	// excerpts (only when they are kept).
	all *redact.Redactor
	// allow finds the terms never masked, lowered.
	allow *redact.Literals
}

// Compile builds an Engine from checked settings; nil when the firewall
// is off.
func Compile(s Settings, env *policy.Env) (*Engine, error) {
	if !s.Enabled {
		return nil, nil
	}
	// Settings saved before the special categories and the model had them.
	if s.Sensitive == "" {
		s.Sensitive = Alert
	}
	if s.Model.FailMode == "" {
		s.Model.FailMode = ModelFailOpen
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
	if len(detectors) == 0 && len(custom) == 0 && len(s.Policies) == 0 && !s.Model.Secrets && !s.Model.PII {
		return nil, nil
	}
	r, err := redact.New(redact.Options{Detectors: detectors, Patterns: custom})
	if err != nil {
		return nil, fmt.Errorf("firewall: %w", err)
	}
	e.r = r
	if len(s.Model.Allow) > 0 {
		terms := make([]string, len(s.Model.Allow))
		for i, t := range s.Model.Allow {
			terms[i] = strings.ToLower(t)
		}
		e.allow = redact.NewLiterals(terms)
	}
	if s.Excerpts {
		all := append(append(slices.Clone(redact.DefaultDetectors), redact.IPv4), redact.NationalIDs...)
		if e.all, err = redact.New(redact.Options{Detectors: all, Patterns: s.Custom}); err != nil {
			return nil, fmt.Errorf("firewall: %w", err)
		}
	}
	return e, nil
}

// Settings are the engine's settings.
func (e *Engine) Settings() Settings { return e.s }

// MaskText masks every finding in plain text (an answer's), whatever the
// classes' actions: [REDACTED:email].
func (e *Engine) MaskText(s string) string {
	masked, _ := e.r.Mask(s)
	return masked
}

func (e *Engine) action(class string) string {
	switch class {
	case ClassSecret:
		return e.s.Secrets
	case ClassPII:
		return e.s.PII
	case ClassSensitive:
		return e.s.Sensitive
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
	// Model reports the detector model's finding, not a pattern's.
	Model bool
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
	// Failed reports a scan that failed and was let through (fail open);
	// ModelFailed the model's, the patterns' findings applied alone;
	// ModelPartial a model that read only the newest of the request's text.
	Failed, ModelFailed, ModelPartial bool
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

// ignored reports a value under a key whose strings hold the protocol's
// identifiers, not text; inside a tool's input those keys are the tool's
// own and are read.
func ignored(w *Walker) bool { return skipped[w.Key()] && !w.InPayload() }

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
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '+' && c != '/' && c != '=' && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// ErrScan is a request the firewall couldn't read.
var ErrScan = errors.New("firewall: the request couldn't be scanned")

// ErrModel is a detector model's answer that doesn't fit the question.
var ErrModel = errors.New("firewall: the detector model's answer doesn't fit")

// BlockedByModel is what blocked a call the detector model couldn't read
// when its fail mode is closed.
const BlockedByModel = "scan:model"

// Inspect scans a request body and applies the classes' actions: the
// policies come after, with Decide. A scan that fails returns ErrScan
// with what fail mode says: a blocked outcome, or one let through.
func (e *Engine) Inspect(body []byte) (*Outcome, error) { return e.InspectCall(body, Scan{}) }

// Scan is what Inspect knows of a call beside its body.
type Scan struct {
	// Conversation is the call's conversation (its first message's
	// anchor) and Key the gateway's token key. With a key, a value's token
	// is named by a keyed hash of the conversation, the detector and the
	// value: the same in every call of the conversation and on every
	// replica, unlinkable across conversations, and nothing a provider can
	// compute from what it sees. Without one, tokens are numbered.
	Conversation wire.Anchor
	Key          []byte
	// Model is the detector model, nil when the gateway has none; Ctx
	// bounds its wait (the caller's deadline).
	Model Reader
	Ctx   context.Context
}

// tokenName names a value's token for a call with a key.
func (c Scan) tokenName(detector, value string) string {
	mac := hmac.New(sha256.New, c.Key)
	mac.Write(c.Conversation[:])
	mac.Write([]byte(detector))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	return detector + "_" + strings.ToLower(tokenEncoding.EncodeToString(mac.Sum(nil)[:5]))
}

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// InspectCall is Inspect for a call: its tokens are named as Scan says.
func (e *Engine) InspectCall(body []byte, call Scan) (*Outcome, error) {
	o := &Outcome{}
	ls, err := leaves(body)
	if err != nil {
		if e.s.FailMode == FailClosed || e.s.FailMode == FailSecretsClosed && e.s.Secrets == Block {
			o.Blocked, o.BlockedBy = true, "scan"
		} else {
			o.Failed = true
		}
		return o, ErrScan
	}
	found := make([][]finding, len(ls))
	for i, l := range ls {
		if !l.Skip && !blob(l.S) {
			found[i] = e.patternFindings(l.S)
		}
	}
	if call.Model != nil && (e.s.Model.Secrets || e.s.Model.PII) {
		if err := e.readModel(call, ls, found); errors.Is(err, ErrPartial) {
			o.ModelPartial = true
		} else if err != nil {
			if e.s.Model.FailMode == ModelFailClosed {
				o.Blocked, o.BlockedBy = true, BlockedByModel
				return o, ErrScan
			}
			o.ModelFailed = true
		}
	}
	sc := scan{e: e, o: o, call: call, hits: map[[2]string]*Hit{}}
	repl := map[int]string{}
	for i, l := range ls {
		if r, ok := sc.apply(l, found[i]); ok {
			repl[i] = r
		}
	}
	for _, k := range sc.order {
		o.Hits = append(o.Hits, *sc.hits[k])
	}
	if len(repl) > 0 {
		o.Body = splice(body, ls, repl)
	}
	return o, nil
}

// scan is one request's: its outcome, and its hits per detector and
// location in the order first found.
type scan struct {
	e     *Engine
	o     *Outcome
	call  Scan
	hits  map[[2]string]*Hit
	order [][2]string
}

// apply takes the findings fs in one value and returns the value with
// their actions applied; false when it is unchanged.
func (sc *scan) apply(l Leaf, fs []finding) (string, bool) {
	if len(fs) == 0 {
		return "", false
	}
	e, o, s := sc.e, sc.o, l.S
	// Everything found in the text, read once if an excerpt is kept.
	var spans []redact.Finding
	var b strings.Builder
	last, changed := 0, false
	for _, f := range fs {
		action := f.Action
		value := s[f.Start:f.End]
		// A value that JSON must escape can't be put back verbatim in a
		// streamed answer's text: it is masked instead.
		if action == Tokenize && !plain(value) {
			action = Redact
		}
		k := [2]string{f.Detector, l.Top}
		h := sc.hits[k]
		if h == nil {
			h = &Hit{Detector: f.Detector, Class: f.Class, Action: action, Location: l.Top, Model: f.Model}
			if e.all != nil {
				if spans == nil {
					spans = e.all.Find(s)
					for _, g := range fs {
						spans = append(spans, redact.Finding{Detector: g.Detector, Start: g.Start, End: g.End})
					}
					slices.SortFunc(spans, func(a, b redact.Finding) int { return a.Start - b.Start })
				}
				h.Excerpt = excerpt(s, redact.Finding{Detector: f.Detector, Start: f.Start, End: f.End}, spans)
			}
			sc.hits[k] = h
			sc.order = append(sc.order, k)
		}
		h.Count++
		b.WriteString(s[last:f.Start])
		switch action {
		case Block:
			o.Blocked, o.BlockedBy = true, f.Class
			b.WriteString(value)
		case Redact:
			b.WriteString("[REDACTED:" + f.Detector + "]")
			changed = true
		case Tokenize:
			if o.Tokens == nil {
				o.Tokens = e.r.Tokenizer()
				if len(sc.call.Key) > 0 {
					o.Tokens = e.r.TokenizerNamed(sc.call.tokenName)
				}
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
}

// readModel asks the model about the leaves it reads and merges its
// findings into found, then makes every value it found consistent across
// the request.
func (e *Engine) readModel(call Scan, ls []Leaf, found [][]finding) error {
	var idx []int
	var texts []string
	for i, l := range ls {
		if modelReads(l) {
			idx, texts = append(idx, i), append(texts, l.S)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	ctx := call.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	spans, err := call.Model.Spans(ctx, texts)
	partial := errors.Is(err, ErrPartial)
	if err != nil && !partial {
		return err
	}
	if len(spans) != len(texts) {
		return ErrModel
	}
	for k, i := range idx {
		found[i] = merge(found[i], e.modelFindings(ls[i].S, spans[k]))
	}
	e.consistent(ls, found)
	if partial {
		return ErrPartial
	}
	return nil
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

// excerpt is up to 40 bytes either side of a finding, every span found in
// the whole text (by every detector and the project's patterns, whatever
// their actions) masked, those the window cuts too.
func excerpt(s string, f redact.Finding, spans []redact.Finding) string {
	start, end := max(0, f.Start-40), min(len(s), f.End+40)
	for start > 0 && start < len(s) && s[start]&0xC0 == 0x80 {
		start--
	}
	for end < len(s) && s[end]&0xC0 == 0x80 {
		end++
	}
	var b strings.Builder
	pos := start
	for _, sp := range spans {
		if sp.End <= pos || sp.Start >= end {
			continue
		}
		if sp.Start > pos {
			b.WriteString(s[pos:sp.Start])
		}
		b.WriteString("[REDACTED:" + sp.Detector + "]")
		pos = min(sp.End, end)
	}
	b.WriteString(s[pos:end])
	return b.String()
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
	if len(e.s.Policies) == 0 || o.BlockedBy == "scan" || o.BlockedBy == BlockedByModel || o.Failed {
		return
	}
	findings, classes := map[string]int64{}, map[string]int64{ClassSecret: 0, ClassPII: 0, ClassSensitive: 0, ClassCustom: 0}
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
	case o.BlockedBy == BlockedByModel:
		return "the firewall's detector model couldn't read the request in time, and this project refuses what it can't read"
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
