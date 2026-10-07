package routers

import (
	"context"
	"fmt"
	"math"
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
	Tiers      []TierReport
	Families   []FamilyReport
	// Ready reports that every cheaper tier the router chose for calls
	// has a quality whose lower bound meets the floor, on enough judged
	// calls.
	Ready bool
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

// BuildReport reads a router's report for [from, to).
func BuildReport(ctx context.Context, db *events.DB, project uuid.UUID, r Router, from, to time.Time, price Pricer) (*Report, error) {
	spec := &r.Spec
	def := spec.Index(spec.Default)
	rep := &Report{Sources: map[string]uint64{}, Tiers: make([]TierReport, len(spec.Tiers))}
	index := make(map[string]int, len(spec.Tiers))
	for i, t := range spec.Tiers {
		rep.Tiers[i] = TierReport{Name: t.Name, Rank: i - def}
		index[t.Name] = i
	}

	var hours []hourly
	if err := db.Select(ctx, &hours, events.Project(project), `SELECT tier, decided_tier, tier_source, shadow,
		sum(calls) AS calls, sum(errors) AS errors, sum(priced) AS priced, sum(cost_micros) AS cost,
		sum(savings_micros) AS savings, sum(input_tokens) AS input, sum(output_tokens) AS output,
		sum(cache_read_tokens) AS cache_read, sum(cache_write_tokens) AS cache_write
		FROM routers_hourly WHERE {tenant} AND router = ? AND hour >= toStartOfHour(?) AND hour < ?
		GROUP BY tier, decided_tier, tier_source, shadow`, r.Name, from, to); err != nil {
		return nil, fmt.Errorf("routers: report calls: %w", err)
	}
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
			rep.Tiers[i].Served += h.Calls
			rep.Tiers[i].CostMicros += h.Cost
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
			u := wire.Usage{Usage: llm.Usage{Input: h.Input, CacheRead: h.CacheIn, CacheWrite: h.CacheOut, Output: h.Output}}
			rep.EstimatedMicros += int64(m.CostMicros(u.Usage))
		}
	}

	var verdicts []struct {
		Tier    string `ch:"candidate_tier"`
		Verdict string `ch:"verdict"`
		N       uint64 `ch:"n"`
	}
	if err := db.Select(ctx, &verdicts, events.Project(project), `SELECT candidate_tier, verdict, count() AS n
		FROM router_evals WHERE {tenant} AND router = ? AND baseline_tier = ? AND ts >= ? AND ts < ?
		GROUP BY candidate_tier, verdict`, r.Name, spec.Default, from, to); err != nil {
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
		FROM router_evals WHERE {tenant} AND router = ? AND ts >= ? AND ts < ?`, r.Name, from, to); err != nil {
		return nil, fmt.Errorf("routers: report evaluation spend: %w", err)
	}
	if len(spent) > 0 {
		rep.EvalMicros = spent[0].Micros
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
		FROM router_evals WHERE {tenant} AND router = ? AND baseline_tier = ? AND ts >= ? AND ts < ?
		GROUP BY family_id, candidate_tier HAVING judged > 0 ORDER BY judged DESC, family_id, candidate_tier LIMIT 100`,
		r.Name, spec.Default, from, to); err != nil {
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
