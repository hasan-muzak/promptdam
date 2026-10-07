// Package tiers is what a model router is made of and how it places a
// call: its tiers, from cheapest to strongest, its rules, its default tier,
// and the cascade that picks one tier for each call (project.md §6.2).
//
// It imports nothing of Promptdam's graph, so the graph's schema can store
// its types: features/routers keeps routers, the resolver runs the cascade.
package tiers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Limits of a router.
const (
	MaxTiers       = 8
	MaxFallbacks   = 5
	MaxExamples    = 20
	MaxExampleLen  = 2000
	MaxDescription = 500
	MaxRules       = 20
)

// Statuses. A shadow router decides and evaluates every call it gets but
// serves its default tier, so its report shows what it would do before it
// does it.
const (
	Shadow   = "shadow"
	Active   = "active"
	Disabled = "disabled"
)

// Sources: the step of the cascade that placed a call.
const (
	// SourceExplicit: the client named the tier (model "router:tier" or
	// X-Promptdam-Tier).
	SourceExplicit = "explicit"
	// SourceRule: one of the router's rules held.
	SourceRule = "rule"
	// SourceClassifier: the classifier trained on the project's judged
	// calls was confident.
	SourceClassifier = "classifier"
	// SourceDecision: the decision model chose, the classifier being unsure.
	SourceDecision = "decision"
	// SourceDefault: nothing placed the call; it got the default tier.
	SourceDefault = "default"
)

// Tier is one level of a router: a model and its fallbacks, and what it is
// for, in the customer's words.
type Tier struct {
	Name string `json:"name"`
	// Description says what work the tier is for; the decision model reads
	// it.
	Description string `json:"description,omitempty"`
	// Model is the tier's model: a model, or provider/model.
	Model string `json:"model"`
	// Fallbacks are tried in order when Model fails.
	Fallbacks []string `json:"fallbacks,omitempty"`
	// Examples are requests the tier is for, as the decision model sees
	// them.
	Examples []string `json:"examples,omitempty"`
}

// Rule sends the calls its condition (CEL over request, as routing rules
// read it) holds for to a tier.
type Rule struct {
	When string `json:"when"`
	Tier string `json:"tier"`
}

// Spec is a router's tiers, rules and default tier.
type Spec struct {
	// Tiers go from cheapest to strongest.
	Tiers []Tier `json:"tiers"`
	Rules []Rule `json:"rules,omitempty"`
	// Default is the tier calls get when nothing places them, and the one
	// the others' answers are judged against: the strongest unless set.
	Default string `json:"default"`
}

// InvalidError is a spec that can't be saved; its message says why.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "tiers: " + e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidError{Reason: fmt.Sprintf(format, args...)}
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Check validates a spec and tidies it (trims names, defaults the default
// tier to the strongest). Rules' conditions are the caller's to compile.
func (s *Spec) Check() error {
	if len(s.Tiers) == 0 || len(s.Tiers) > MaxTiers {
		return invalid("a router has 1 to %d tiers", MaxTiers)
	}
	seen := make(map[string]bool, len(s.Tiers))
	for i := range s.Tiers {
		t := &s.Tiers[i]
		t.Name, t.Model, t.Description = strings.TrimSpace(t.Name), strings.TrimSpace(t.Model), strings.TrimSpace(t.Description)
		switch {
		case !nameRE.MatchString(t.Name):
			return invalid("%q: a tier's name is 1 to 32 lowercase letters, digits, - or _", t.Name)
		case seen[t.Name]:
			return invalid("two tiers are named %q", t.Name)
		case t.Model == "" || len(t.Model) > 300:
			return invalid("tier %s: model is a model, or provider/model", t.Name)
		case len(t.Description) > MaxDescription:
			return invalid("tier %s: a description is at most %d characters", t.Name, MaxDescription)
		case len(t.Fallbacks) > MaxFallbacks:
			return invalid("tier %s: at most %d fallbacks", t.Name, MaxFallbacks)
		case len(t.Examples) > MaxExamples:
			return invalid("tier %s: at most %d examples", t.Name, MaxExamples)
		}
		seen[t.Name] = true
		for j, f := range t.Fallbacks {
			f = strings.TrimSpace(f)
			if f == "" || len(f) > 300 || strings.HasPrefix(f, "/") || strings.HasSuffix(f, "/") {
				return invalid("tier %s: %q: a fallback is a model, or provider/model", t.Name, f)
			}
			t.Fallbacks[j] = f
		}
		for j, e := range t.Examples {
			e = strings.TrimSpace(e)
			if e == "" || len(e) > MaxExampleLen {
				return invalid("tier %s: an example is 1 to %d characters", t.Name, MaxExampleLen)
			}
			t.Examples[j] = e
		}
	}
	s.Default = strings.TrimSpace(s.Default)
	if s.Default == "" {
		s.Default = s.Tiers[len(s.Tiers)-1].Name
	}
	if !seen[s.Default] {
		return invalid("the default tier %q is not one of the router's", s.Default)
	}
	if len(s.Rules) > MaxRules {
		return invalid("a router has at most %d rules", MaxRules)
	}
	for i := range s.Rules {
		r := &s.Rules[i]
		r.Tier = strings.TrimSpace(r.Tier)
		if !seen[r.Tier] {
			return invalid("rule %d sends calls to %q, not one of the router's tiers", i+1, r.Tier)
		}
		if strings.TrimSpace(r.When) == "" || len(r.When) > 4096 {
			return invalid("rule %d: a condition is 1 to 4096 characters", i+1)
		}
	}
	return nil
}

// Index is the position of the tier named name, -1 when there is none. A
// router has at most MaxTiers.
func (s *Spec) Index(name string) int {
	for i, t := range s.Tiers {
		if t.Name == name {
			return i
		}
	}
	return -1
}

// ErrUnknownTier refuses a call that names a tier the router hasn't.
var ErrUnknownTier = errors.New("tiers: the router has no such tier")

// Call is what the cascade knows of a call.
type Call struct {
	// Pin is the tier the client named ("" for none).
	Pin string
	// Holds evaluates a rule's condition on the call; one that fails to
	// evaluate doesn't hold.
	Holds func(when string) bool
	// Serves reports whether tier i's model can take the call: its
	// features and context window against what the call needs.
	Serves func(i int) bool
	// Learned is the classifier's placement, when the router has one: a
	// tier ("" for none) and the probability it is right, and whether the
	// classifier is unsure (then Decide is asked).
	Learned func() (tier string, p float64, unsure bool)
	// Decide asks the decision model for a tier ("" when it didn't
	// answer in time).
	Decide func() string
}

// Choice is the tier the cascade picked and why.
type Choice struct {
	Tier   int
	Source string
	// Rule is the index of the rule that held (-1: none).
	Rule int
	// Confidence is how sure the step that placed the call was: 1 for the
	// client's choice and rules, a probability for the classifier, 0 for
	// the default.
	Confidence float64
	// Moved reports that the tier first chosen couldn't take the call, and
	// the nearest one that can (stronger first) got it.
	Moved bool
	// Unsure reports a call the classifier was unsure of and no decision
	// model placed: it got the default tier.
	Unsure bool
}

// Choose runs the cascade: the client's tier, else the first rule that
// holds, else the classifier's tier, else (when the classifier is unsure)
// the decision model's, else the default tier; then the requirements,
// which move a call its tier can't serve to the next stronger tier that
// can (a weaker one only when no stronger can).
func (s *Spec) Choose(c Call) (Choice, error) {
	ch := Choice{Tier: s.Index(s.Default), Source: SourceDefault, Rule: -1}
	placed := false
	if c.Pin != "" {
		i := s.Index(c.Pin)
		if i < 0 {
			return ch, ErrUnknownTier
		}
		ch, placed = Choice{Tier: i, Source: SourceExplicit, Rule: -1, Confidence: 1}, true
	}
	if !placed && c.Holds != nil {
		for k, r := range s.Rules {
			if c.Holds(r.When) {
				ch, placed = Choice{Tier: s.Index(r.Tier), Source: SourceRule, Rule: k, Confidence: 1}, true
				break
			}
		}
	}
	unsure := false
	if !placed && c.Learned != nil {
		tier, p, u := c.Learned()
		if i := s.Index(tier); i >= 0 {
			ch, placed = Choice{Tier: i, Source: SourceClassifier, Rule: -1, Confidence: p}, true
		}
		unsure = u
	}
	if !placed && unsure && c.Decide != nil {
		if i := s.Index(c.Decide()); i >= 0 {
			ch, placed = Choice{Tier: i, Source: SourceDecision, Rule: -1}, true
		}
	}
	ch.Unsure = !placed && unsure
	if c.Serves == nil || c.Serves(ch.Tier) {
		return ch, nil
	}
	for i := ch.Tier + 1; i < len(s.Tiers); i++ {
		if c.Serves(i) {
			ch.Tier, ch.Moved = i, true
			return ch, nil
		}
	}
	for i := ch.Tier - 1; i >= 0; i-- {
		if c.Serves(i) {
			ch.Tier, ch.Moved = i, true
			return ch, nil
		}
	}
	return ch, nil // none can: the provider will say why
}

// ParseModel splits a model a client asked for into a router's name and a
// tier: "auto" and "auto:fast". ok is false when there's no router by that
// name (routers is the project's, by name).
func ParseModel[V any](model string, routers map[string]V) (router V, pin string, ok bool) {
	name, pin, _ := strings.Cut(model, ":")
	router, ok = routers[name]
	return router, pin, ok
}
