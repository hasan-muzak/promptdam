package routers

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/classifier"
)

// MinJudged is how many judged calls a tier needs before its quality says
// anything (the report's ready and the canary).
const MinJudged = 50

// Report is how a router did over a time range: where its calls went and
// why, what they cost and would have cost on the tiers it chose, and each
// tier's quality against the default tier on the calls judged.
type Report struct {
	Calls, Errors uint64
	// CostMicros is what the calls cost; EstimatedMicros what they would
	// have cost on the tiers the router chose (in shadow mode, those it
	// would have served), with the same tokens. Unpriced calls' chosen
	// tier has no price; they are left out of the estimate.
	CostMicros, EstimatedMicros int64
	Unpriced                    uint64
	SavingsMicros               int64
	// EvalMicros is what evaluation (replays, judging) spent.
	EvalMicros int64
	Sources    map[string]uint64
	// Decisions are the calls the decision model was asked about, by how
	// it went, most first: a model that often times out leaves its calls
	// on the default tier.
	Decisions []DecisionReport
	Tiers     []TierReport
	Families  []FamilyReport
	// Ready reports that every cheaper tier the router chose for calls
	// has a quality whose lower bound meets the floor, on enough judged
	// calls.
	Ready bool
}

// DecisionReport is the decision model's calls that went one way
// (resolver.Decision… outcomes).
type DecisionReport struct {
	Outcome    string `ch:"decision"`
	Calls      uint64 `ch:"calls"`
	CostMicros int64  `ch:"router_micros"`
	// WaitMs are how long the calls waited, in milliseconds: the median,
	// the 95th and the 99th percentiles.
	WaitMs []float64 `ch:"wait_ms"`
}

// TierReport is one tier's part.
type TierReport struct {
	Name string
	// Rank is the tier's place against the default tier: negative for
	// cheaper tiers, positive for stronger ones.
	Rank int
	// Served counts the calls the tier answered; Decided those the router
	// chose it for.
	Served, Decided uint64
	CostMicros      int64
	// Input are the input tokens of the calls it answered, CacheRead those
	// read from providers' caches; Models its calls by the model that
	// answered them, its own first, then its fallbacks by calls.
	Input, CacheRead uint64
	Models           []TierModel
	// Judged calls and their verdicts against the default tier;
	// Unjudged those that needed a judge and got none.
	Judged, Wins, Ties, Losses, Unjudged uint64
	// Quality is, for a cheaper tier, the share of answers as good as the
	// default tier's or better; for a stronger tier, the share that were
	// better (the calls needed it). Low and High bound it (Wilson, 95%).
	Quality, Low, High float64
	// MeetsFloor reports that Low is at least the router's floor, on at
	// least MinJudged judged calls (cheaper tiers only).
	MeetsFloor bool
	// JudgeLeans names the side the judge may favor when the tier's model
	// and the default tier's come from different vendors and the judge's
	// from one of them: "tier" or "default" ("" otherwise). Models judged
	// against their own vendor's tend to win.
	JudgeLeans string
	// FlipRate is how often the router's judge disagreed with itself when
	// asked both ways; JudgeBoth that every verdict is judged both ways.
	FlipRate  float64
	JudgeBoth bool
}

// TierModel is the calls of a tier one model answered: the tier's own, or
// one of its fallbacks (a provider's, the model the gateway asked for).
// Calls from before models were counted have neither.
type TierModel struct {
	Provider, Model  string
	Fallback         bool
	Calls            uint64
	CostMicros       int64
	Input, CacheRead uint64
}

// CacheRate is the share of the calls' input read from providers' caches.
func (m TierModel) CacheRate() float64 { return rate(m.CacheRead, m.Input) }

// CostPerCall is what a call cost on average, in micro-dollars.
func (m TierModel) CostPerCall() int64 { return perCall(m.CostMicros, m.Calls) }

// CacheRate is the share of the tier's input read from providers' caches.
func (t TierReport) CacheRate() float64 { return rate(t.CacheRead, t.Input) }

// CostPerCall is what a call the tier answered cost on average, in
// micro-dollars.
func (t TierReport) CostPerCall() int64 { return perCall(t.CostMicros, t.Served) }

func rate(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

func perCall(micros int64, calls uint64) int64 {
	if calls == 0 {
		return 0
	}
	return int64(math.Round(float64(micros) / float64(calls)))
}

// own reports whether a tier's model reference (a model, or
// provider/model) names the model a provider was asked for.
func own(ref, provider, model string) bool {
	return ref == model || ref == provider+"/"+model
}

// FamilyReport is one family's judged calls on one tier: right are, for
// a tier cheaper than the default, the answers as good or better; for a
// stronger one, the better answers.
type FamilyReport struct {
	Family, Tier  string
	Judged, Right uint64
	Quality, Low  float64
	Cheaper       bool
	MeetsFloor    bool
}

// Pricer prices a tier's model for estimates ("model" or
// "provider/model"); ok is false when it has no price.
type Pricer func(model string) (llm.Model, bool)

type hourly struct {
	Tier     string `ch:"tier"`
	Decided  string `ch:"decided_tier"`
	Source   string `ch:"tier_source"`
	Shadow   bool   `ch:"shadow"`
	Provider string `ch:"provider"`
	Model    string `ch:"target_model"`
	Calls    uint64 `ch:"calls"`
	Errors   uint64 `ch:"errors"`
	Priced   uint64 `ch:"priced"`
	Cost     int64  `ch:"cost"`
	Savings  int64  `ch:"savings"`
	Input    uint64 `ch:"input"`
	Output   uint64 `ch:"output"`
	CacheIn  uint64 `ch:"cache_read"`
	CacheOut uint64 `ch:"cache_write"`
}

// writes is how many output tokens the default tier writes for each one
// tier writes, measured on the router's judged calls (1 for the default
// tier, or when not measured).
func writes(r Router, tier string) float64 {
	if tier == r.Spec.Default {
		return 1
	}
	if ratio, ok := r.Classifier.Tier(tier).Ratio(); ok {
		return ratio
	}
	return 1
}

// vendors are model families by their name's start.
var vendors = []struct{ prefix, vendor string }{
	{"claude", "anthropic"}, {"gpt", "openai"}, {"o1", "openai"}, {"o3", "openai"}, {"o4", "openai"}, {"chatgpt", "openai"},
	{"gemini", "google"}, {"gemma", "google"}, {"mistral", "mistral"}, {"ministral", "mistral"}, {"magistral", "mistral"},
	{"codestral", "mistral"}, {"deepseek", "deepseek"}, {"grok", "xai"}, {"llama", "meta"}, {"qwen", "alibaba"},
}

// vendorOf is the vendor of a model ("provider/model" or a model), "" when
// unknown.
func vendorOf(model string) string {
	name := strings.ToLower(model)
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	for _, v := range vendors {
		if strings.HasPrefix(name, v.prefix) {
			return v.vendor
		}
	}
	return ""
}

// defaultJudge is the judge evaluation uses when a router names none and
// a provider of the project serves it.
const defaultJudge = "claude-sonnet-5-5"

// leans is the side a judge may favor between a tier's model and the
// default's: the one from its vendor, when they differ.
func leans(judge, tier, def string) string {
	j, t, d := vendorOf(judge), vendorOf(tier), vendorOf(def)
	switch {
	case j == "" || t == "" || d == "" || t == d:
		return ""
	case j == t:
		return "tier"
	case j == d:
		return "default"
	}
	return ""
}

// BuildReport reads a router's report for [from, to). Times of judged
// calls are bound in milliseconds: ClickHouse compares a DateTime64 column
// with a whole-second DateTime wrongly within that second.
func BuildReport(ctx context.Context, db *events.DB, project uuid.UUID, r Router, from, to time.Time, price Pricer) (*Report, error) {
	spec := &r.Spec
	def := spec.Index(spec.Default)
	rep := &Report{Sources: map[string]uint64{}, Tiers: make([]TierReport, len(spec.Tiers))}
	index := make(map[string]int, len(spec.Tiers))
	judge := r.Judge
	if judge == "" {
		judge = defaultJudge
	}
	for i, t := range spec.Tiers {
		rep.Tiers[i] = TierReport{Name: t.Name, Rank: i - def}
		if i != def {
			rep.Tiers[i].JudgeLeans = leans(judge, t.Model, spec.Tiers[def].Model)
		}
		if r.Classifier != nil {
			rep.Tiers[i].FlipRate, rep.Tiers[i].JudgeBoth = r.Classifier.FlipRate, r.Classifier.JudgeBoth
		}
		index[t.Name] = i
	}

	var hours []hourly
	if err := db.Select(ctx, &hours, events.Project(project), `SELECT tier, decided_tier, tier_source, shadow, provider,
		target_model, sum(calls) AS calls, sum(errors) AS errors, sum(priced) AS priced, sum(cost_micros) AS cost,
		sum(savings_micros) AS savings, sum(input_tokens) AS input, sum(output_tokens) AS output,
		sum(cache_read_tokens) AS cache_read, sum(cache_write_tokens) AS cache_write
		FROM routers_hourly WHERE {tenant} AND router = ? AND hour >= toStartOfHour(?) AND hour < ?
		GROUP BY tier, decided_tier, tier_source, shadow, provider, target_model`, r.Name, from, to); err != nil {
		return nil, fmt.Errorf("routers: report calls: %w", err)
	}
	models := map[[3]string]*TierModel{}
	prices := map[string]*llm.Model{}
	priceOf := func(tier string) *llm.Model {
		i, ok := index[tier]
		if !ok || price == nil {
			return nil
		}
		if m, seen := prices[tier]; seen {
			return m
		}
		var m *llm.Model
		if p, ok := price(spec.Tiers[i].Model); ok {
			p.Tiers = nil // sums of many calls would reach a long-prompt price
			m = &p
		}
		prices[tier] = m
		return m
	}
	for _, h := range hours {
		rep.Calls += h.Calls
		rep.Errors += h.Errors
		rep.CostMicros += h.Cost
		rep.SavingsMicros += h.Savings
		source := h.Source
		if h.Shadow {
			source = "shadow:" + source
		}
		rep.Sources[source] += h.Calls
		if i, ok := index[h.Tier]; ok {
			t := &rep.Tiers[i]
			t.Served += h.Calls
			t.CostMicros += h.Cost
			t.Input += h.Input
			t.CacheRead += h.CacheIn
			m := models[[3]string{h.Tier, h.Provider, h.Model}]
			if m == nil {
				m = &TierModel{Provider: h.Provider, Model: h.Model,
					Fallback: h.Model != "" && !own(spec.Tiers[i].Model, h.Provider, h.Model)}
				models[[3]string{h.Tier, h.Provider, h.Model}] = m
			}
			m.Calls += h.Calls
			m.CostMicros += h.Cost
			m.Input += h.Input
			m.CacheRead += h.CacheIn
		}
		if i, ok := index[h.Decided]; ok {
			rep.Tiers[i].Decided += h.Calls
		}
		switch m := priceOf(h.Decided); {
		case h.Decided == h.Tier:
			rep.EstimatedMicros += h.Cost
		case m == nil:
			rep.Unpriced += h.Priced
		default:
			// The chosen tier, priced on the call's input with no cache (its
			// model's cache isn't the served one's: a projection that errs
			// low on savings), writing what it writes against the served
			// tier on the router's judged calls.
			out := float64(h.Output) * writes(r, h.Tier) / writes(r, h.Decided)
			u := wire.Usage{Usage: llm.Usage{Input: h.Input, Output: uint64(math.Round(out))}}
			rep.EstimatedMicros += int64(m.CostMicros(u.Usage))
		}
	}

	for k, m := range models {
		t := &rep.Tiers[index[k[0]]]
		t.Models = append(t.Models, *m)
	}
	for i := range rep.Tiers {
		slices.SortFunc(rep.Tiers[i].Models, func(a, b TierModel) int {
			if a.Fallback != b.Fallback {
				if a.Fallback {
					return 1
				}
				return -1
			}
			return cmp.Or(cmp.Compare(b.Calls, a.Calls), cmp.Compare(a.Provider+"/"+a.Model, b.Provider+"/"+b.Model))
		})
	}

	var verdicts []struct {
		Tier    string `ch:"candidate_tier"`
		Verdict string `ch:"verdict"`
		N       uint64 `ch:"n"`
	}
	if err := db.Select(ctx, &verdicts, events.Project(project), `SELECT candidate_tier, verdict, count() AS n
		FROM router_evals WHERE {tenant} AND router = ? AND baseline_tier = ? AND ts >= fromUnixTimestamp64Milli(?)
		AND ts < fromUnixTimestamp64Milli(?) GROUP BY candidate_tier, verdict`, r.Name, spec.Default, from.UnixMilli(), to.UnixMilli()); err != nil {
		return nil, fmt.Errorf("routers: report verdicts: %w", err)
	}
	for _, v := range verdicts {
		i, ok := index[v.Tier]
		if !ok {
			continue
		}
		t := &rep.Tiers[i]
		switch v.Verdict {
		case "win":
			t.Wins += v.N
		case "tie":
			t.Ties += v.N
		case "loss":
			t.Losses += v.N
		default:
			t.Unjudged += v.N
		}
	}
	rep.Ready = true
	judgedAny := false
	for i := range rep.Tiers {
		t := &rep.Tiers[i]
		t.Judged = t.Wins + t.Ties + t.Losses
		right := t.Wins
		if t.Rank < 0 {
			right += t.Ties
		}
		if t.Judged > 0 {
			t.Quality = float64(right) / float64(t.Judged)
			t.Low, t.High = classifier.Wilson(int(right), int(t.Judged))
		}
		t.MeetsFloor = t.Rank < 0 && t.Judged >= MinJudged && t.Low >= r.QualityFloor
		judgedAny = judgedAny || t.Judged >= MinJudged
		if t.Rank < 0 && t.Decided > 0 && !t.MeetsFloor {
			rep.Ready = false
		}
	}
	rep.Ready = rep.Ready && judgedAny

	var spent []struct {
		Micros int64 `ch:"micros"`
	}
	if err := db.Select(ctx, &spent, events.Project(project), `SELECT sum(candidate_micros + baseline_micros + judge_micros) AS micros
		FROM router_evals WHERE {tenant} AND router = ? AND ts >= fromUnixTimestamp64Milli(?) AND ts < fromUnixTimestamp64Milli(?)`,
		r.Name, from.UnixMilli(), to.UnixMilli()); err != nil {
		return nil, fmt.Errorf("routers: report evaluation spend: %w", err)
	}
	if len(spent) > 0 {
		rep.EvalMicros = spent[0].Micros
	}

	rep.Decisions = []DecisionReport{}
	if err := db.Select(ctx, &rep.Decisions, events.Project(project), `SELECT decision, sum(calls) AS calls,
		sum(router_micros) AS router_micros, quantilesTDigestMerge(0.5, 0.95, 0.99)(decision_ms) AS wait_ms
		FROM decisions_hourly WHERE {tenant} AND router = ? AND hour >= toStartOfHour(?) AND hour < ?
		GROUP BY decision ORDER BY calls DESC, decision`, r.Name, from, to); err != nil {
		return nil, fmt.Errorf("routers: report decisions: %w", err)
	}

	var fams []struct {
		Family string `ch:"family_id"`
		Tier   string `ch:"candidate_tier"`
		Judged uint64 `ch:"judged"`
		Held   uint64 `ch:"held"`
		Wins   uint64 `ch:"wins"`
	}
	if err := db.Select(ctx, &fams, events.Project(project), `SELECT family_id, candidate_tier,
		countIf(verdict != '') AS judged, countIf(verdict IN ('win', 'tie')) AS held, countIf(verdict = 'win') AS wins
		FROM router_evals WHERE {tenant} AND router = ? AND baseline_tier = ? AND ts >= fromUnixTimestamp64Milli(?)
		AND ts < fromUnixTimestamp64Milli(?)
		GROUP BY family_id, candidate_tier HAVING judged > 0 ORDER BY judged DESC, family_id, candidate_tier LIMIT 100`,
		r.Name, spec.Default, from.UnixMilli(), to.UnixMilli()); err != nil {
		return nil, fmt.Errorf("routers: report families: %w", err)
	}
	for _, f := range fams {
		i, ok := index[f.Tier]
		if !ok {
			continue
		}
		cheaper := i < def
		right := f.Wins
		if cheaper {
			right = f.Held
		}
		low, _ := classifier.Wilson(int(right), int(f.Judged))
		rep.Families = append(rep.Families, FamilyReport{Family: f.Family, Tier: f.Tier, Judged: f.Judged, Right: right,
			Quality: float64(right) / float64(f.Judged), Low: low, Cheaper: cheaper,
			MeetsFloor: cheaper && f.Judged >= MinJudged && low >= r.QualityFloor})
	}
	return rep, nil
}

// Monthly scales an amount over a range to 30 days.
func Monthly(micros int64, from, to time.Time) int64 {
	d := to.Sub(from)
	if d <= 0 {
		return 0
	}
	return int64(math.Round(float64(micros) * float64(30*24*time.Hour) / float64(d)))
}
