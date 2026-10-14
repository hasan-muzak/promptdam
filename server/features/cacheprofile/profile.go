// Package cacheprofile learns how each model uses its prompt cache, from
// every project's calls pooled (decided 2026-10-14: only counts and rates
// per kind, model and backend are pooled, never what the calls said), and
// plans each call's caching by it: which strategy (the gateway's
// breakpoints, or the provider's own caching), whether its conversation
// tries the other one while evidence is missing (at most TrialShare of a
// model's conversations), and which backends of a router of providers to
// keep it off (those that never cache while another does). Models are
// never named in code: a model released today is planned from its price
// list, then from what its calls show.
package cacheprofile

import (
	"hash/fnv"
	"slices"
	"time"

	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// TrialShare is the percentage of a model's conversations that try the
// strategy its profile is learning about.
const TrialShare = 10

// Doc is every model's profile, as the learner last wrote it.
type Doc struct {
	Learned time.Time `json:"learned"`
	// Models are the profiles by Key(kind, model).
	Models map[string]Profile `json:"models"`
	// Endpoints are routers of providers' backends' names and the slugs
	// that name them in requests, by model (OpenRouter's "Google AI
	// Studio" is "google-ai-studio"), cached for the learner.
	Endpoints map[string]Endpoints `json:"endpoints,omitempty"`
}

// Profile is how a model of a provider kind uses its prompt cache.
type Profile struct {
	// Use is the strategy its calls are sent with; Trial the one a share
	// of its conversations tries ("" for none).
	Use   string `json:"use"`
	Trial string `json:"trial,omitempty"`
	// Avoid are backends of a router of providers (their slugs) that never
	// cached the model while another did; Backends how many others served
	// it (a conversation is kept on one when there are several).
	Avoid    []string `json:"avoid,omitempty"`
	Backends int      `json:"backends,omitempty"`
	// ReadShare is the share of the tokens Use's calls should have read
	// from the cache that they did; Evidence how many calls expected one;
	// Projects how many projects' calls it was learned from.
	ReadShare float64 `json:"read_share"`
	Evidence  int64   `json:"evidence"`
	Projects  int     `json:"projects"`
}

// Endpoints are a model's backends' slugs by name, when they were read.
type Endpoints struct {
	Fetched time.Time         `json:"fetched"`
	Slugs   map[string]string `json:"slugs"`
}

// shareEvidence is the calls expecting a cache read a model's read share
// is trusted from, to price it.
const shareEvidence = 100

// ReadShare is the share of what a model's calls should read from its
// cache that they do, to price it: 1 (a cache that works) until enough
// calls show otherwise.
func (d *Doc) ReadShare(kind, model string) float64 {
	p, ok := d.Of(kind, model)
	if !ok || p.Evidence < shareEvidence {
		return 1
	}
	return p.ReadShare
}

// Of is a model's profile, when one was learned.
func (d *Doc) Of(kind, model string) (Profile, bool) {
	if d == nil {
		return Profile{}, false
	}
	p, ok := d.Models[Key(kind, model)]
	return p, ok
}

// Shown is a model's profile as a project may be shown it: one learned
// from MinProjects projects or more, never one other project's traffic.
func (d *Doc) Shown(kind, model string) (Profile, bool) {
	p, ok := d.Of(kind, model)
	return p, ok && p.Projects >= MinProjects
}

// Key is a profile's key: a model is profiled per provider kind.
func Key(kind, model string) string { return kind + "/" + model }

// Plan is a call's prompt-cache plan for a target: the strategy its
// model's profile uses, else def (the target's default, from its price
// list); the trial strategy for TrialShare of the model's conversations
// (decided by the conversation, so a conversation keeps its arm on every
// replica); never one the provider refused (off: Store.Refused); the
// backends to keep it off, and whether to keep its conversation on one
// (several serve the model). Strategies its kind doesn't take are ignored.
func (d *Doc) Plan(kind string, anthropic bool, model, def, conversation string, inTurn bool, off []string) gateway.CachePlan {
	strategies := gateway.CacheDialectOf(kind, anthropic).Strategies
	plan := gateway.CachePlan{Strategy: def}
	var p Profile
	if d != nil {
		p = d.Models[Key(kind, model)]
	}
	refused := func(s string) bool { return slices.Contains(off, s) }
	if p.Use != "" && slices.Contains(strategies, p.Use) {
		plan.Strategy = p.Use
	}
	if p.Trial != "" && p.Trial != plan.Strategy && inTurn && slices.Contains(strategies, p.Trial) && !refused(p.Trial) &&
		Arm(conversation, model) {
		plan.Strategy, plan.Trial = p.Trial, true
	}
	if refused(plan.Strategy) {
		plan.Trial = false
		for _, s := range strategies {
			if !refused(s) {
				plan.Strategy = s
				break
			}
		}
	}
	plan.Avoid, plan.Session = p.Avoid, p.Backends > 1
	return plan
}

// Arm reports a conversation that tries its model's trial strategy: the
// same answer on every replica, for TrialShare of conversations.
func Arm(conversation, model string) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(conversation))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(model))
	return h.Sum32()%100 < TrialShare
}
