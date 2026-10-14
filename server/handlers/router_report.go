package handlers

import (
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
	"github.com/hasan-muzak/promptdam/server/features/training"
)

// RouterReportIn is a router and a time range.
type RouterReportIn struct {
	RouterPath
	TimeRange
}

// ClassifierTierOut is how a tier's classifier did on held-out calls.
type ClassifierTierOut struct {
	Threshold float64 `json:"threshold" doc:"The probability from which the gateway places calls on the tier; 0: none yet"`
	Precision float64 `json:"precision" doc:"Held out: the share of calls over the threshold the tier was right for"`
	Coverage  float64 `json:"coverage" doc:"Held out: the share of calls over the threshold"`
	AUC       float64 `json:"auc" doc:"How well it ranks right calls over wrong ones (0.5: chance)"`
	Samples   int     `json:"samples" doc:"Judged calls it was trained on"`
}

// RouterReportTierOut is one tier's part of a report.
type RouterReportTierOut struct {
	Name        string             `json:"name"`
	Model       string             `json:"model"`
	Rank        int                `json:"rank" doc:"Place against the default tier: negative cheaper, positive stronger"`
	Served      uint64             `json:"served" doc:"Calls the tier answered"`
	Decided     uint64             `json:"decided" doc:"Calls the router chose it for (in shadow mode, would have)"`
	CostMicros  int64              `json:"cost_micros"`
	Judged      uint64             `json:"judged" doc:"Calls whose answer on this tier was judged against the default tier's"`
	Wins        uint64             `json:"wins"`
	Ties        uint64             `json:"ties"`
	Losses      uint64             `json:"losses"`
	Unjudged    uint64             `json:"unjudged" doc:"Calls only a judge could decide that got none"`
	Quality     float64            `json:"quality" doc:"Cheaper tiers: the share of answers as good or better; stronger tiers: the share better (needed)"`
	QualityLow  float64            `json:"quality_low" doc:"95% interval (Wilson)"`
	QualityHigh float64            `json:"quality_high"`
	MeetsFloor  bool               `json:"meets_floor" doc:"Cheaper tiers: the interval's low end meets the quality floor, on 50 judged calls or more"`
	Demoted     bool               `json:"demoted" doc:"The canary demoted the tier: the router places no call on it until restored"`
	Classifier  *ClassifierTierOut `json:"classifier"`
	JudgeLeans  string             `json:"judge_leans" doc:"tier or default: the judge's vendor makes one of the two models, the other's another vendor's; models judged against their own vendor's tend to win. Pick a judge from a third vendor for this comparison"`
	FlipRate    float64            `json:"flip_rate" doc:"How often the judge disagreed with itself when asked in both orders"`
	JudgeBoth   bool               `json:"judge_both" doc:"Every verdict is judged in both orders: the judge flipped too often"`
}

// RouterReportFamilyOut is a family's judged calls on one tier.
type RouterReportFamilyOut struct {
	Family     string  `json:"family"`
	Tier       string  `json:"tier"`
	Judged     uint64  `json:"judged"`
	Quality    float64 `json:"quality"`
	QualityLow float64 `json:"quality_low"`
	MeetsFloor bool    `json:"meets_floor"`
}

// RouterReportOut is how a router did.
type RouterReportOut struct {
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	Router       string    `json:"router"`
	Status       string    `json:"status"`
	DefaultTier  string    `json:"default_tier"`
	QualityFloor float64   `json:"quality_floor"`
	Calls        uint64    `json:"calls"`
	Errors       uint64    `json:"errors"`
	CostMicros   int64     `json:"cost_micros" doc:"What the calls cost"`
	// EstimatedMicros and RoutingMicros say what the router's choices are
	// worth, active or in shadow mode.
	EstimatedMicros      int64                   `json:"estimated_micros" doc:"What they would have cost on the tiers the router chose, with the same tokens"`
	RoutingMicros        int64                   `json:"routing_micros" doc:"cost_micros − estimated_micros: what following the router saves (in shadow mode, would save)"`
	MonthlyRoutingMicros int64                   `json:"monthly_routing_micros" doc:"routing_micros over 30 days at this range's pace"`
	LedgerSavingsMicros  int64                   `json:"ledger_savings_micros" doc:"The savings ledger's: against the default tier (or the model asked for) at list price without any cache"`
	Unpriced             uint64                  `json:"unpriced" doc:"Calls whose chosen tier has no price, left out of the estimate"`
	EvalMicros           int64                   `json:"eval_micros" doc:"What evaluation (replays, judging) spent on the project's keys"`
	Ready                bool                    `json:"ready" doc:"Every cheaper tier the router chose meets the quality floor, on enough judged calls"`
	Sources              map[string]uint64       `json:"sources" doc:"Calls by the step that chose their tier (shadow: first when the router only watched)"`
	Decisions            []RouterDecisionOut     `json:"decisions" doc:"The calls the decision model was asked about, by how it answered"`
	Tiers                []RouterReportTierOut   `json:"tiers"`
	Families             []RouterReportFamilyOut `json:"families" doc:"The families judged most, per tier"`
	ClassifierVersion    int                     `json:"classifier_version" doc:"0: not trained yet"`
	ClassifierTrainedAt  *time.Time              `json:"classifier_trained_at"`
}

// RouterDecisionOut is the decision model's calls that went one way.
type RouterDecisionOut struct {
	Outcome    string  `json:"outcome" doc:"answered, cached (an answer to the same question reused), timeout, error, or unreadable (it named no tier)"`
	Calls      uint64  `json:"calls"`
	CostMicros int64   `json:"cost_micros" doc:"What the decision model cost for them"`
	WaitP50Ms  float64 `json:"wait_p50_ms" doc:"How long the calls waited: the median"`
	WaitP95Ms  float64 `json:"wait_p95_ms"`
	WaitP99Ms  float64 `json:"wait_p99_ms"`
}

// GetRouterReport reports how a router did.
func (h *Handlers) GetRouterReport(ctx *muzak.Context, in RouterReportIn) (RouterReportOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	row, err := h.router(ctx, in.Router)
	if err != nil {
		return RouterReportOut{}, err
	}
	from, to, err := in.bounds(7 * 24 * time.Hour)
	if err != nil {
		return RouterReportOut{}, err
	}
	if !from.Before(to) || to.Sub(from) > 90*24*time.Hour {
		return RouterReportOut{}, muzak.UnprocessableEntity("the time range must start before it ends and cover at most 90 days").
			WithCode(CodeInvalidQuery)
	}
	deps := h.RouterDeps
	if deps.Events == nil {
		return RouterReportOut{}, muzak.ServiceUnavailable("the request log isn't available")
	}
	d, err := deps.Sources.Build(ctx.Context(), pa.OrgID(), pa.Project.ID)
	if err != nil {
		return RouterReportOut{}, err
	}
	snap := snapshot.Plain(pa.Project.ID, d)
	price := func(model string) (llm.Model, bool) {
		ts := deps.Explainer.Targets(snap, wire.OpenAIChat, model)
		if len(ts) == 0 {
			return llm.Model{}, false
		}
		return requestlog.Price(deps.Prices, snap.Prices, ts[0].Provider, ts[0].Kind, ts[0].Model)
	}
	rt := routers.Of(row)
	rep, err := routers.BuildReport(ctx.Context(), deps.Events, pa.Project.ID, rt, from, to, price)
	if err != nil {
		return RouterReportOut{}, err
	}
	out := RouterReportOut{From: from, To: to, Router: rt.Name, Status: rt.Status, DefaultTier: rt.Spec.Default,
		QualityFloor: rt.QualityFloor, Calls: rep.Calls, Errors: rep.Errors, CostMicros: rep.CostMicros,
		EstimatedMicros: rep.EstimatedMicros, RoutingMicros: rep.CostMicros - rep.EstimatedMicros,
		LedgerSavingsMicros: rep.SavingsMicros, Unpriced: rep.Unpriced, EvalMicros: rep.EvalMicros, Ready: rep.Ready,
		Sources: rep.Sources, Tiers: make([]RouterReportTierOut, len(rep.Tiers)), Families: []RouterReportFamilyOut{},
		Decisions: make([]RouterDecisionOut, len(rep.Decisions))}
	for i, d := range rep.Decisions {
		out.Decisions[i] = RouterDecisionOut{Outcome: d.Outcome, Calls: d.Calls, CostMicros: d.CostMicros}
		if len(d.WaitMs) == 3 {
			out.Decisions[i].WaitP50Ms, out.Decisions[i].WaitP95Ms, out.Decisions[i].WaitP99Ms = d.WaitMs[0], d.WaitMs[1], d.WaitMs[2]
		}
	}
	out.MonthlyRoutingMicros = routers.Monthly(out.RoutingMicros, from, to)

	cls, err := h.Routers.Classifier(ctx.Context(), row.ID)
	if err != nil {
		return RouterReportOut{}, err
	}
	byTier := map[string]*ClassifierTierOut{}
	if cls != nil && cls.Model.Default == rt.Spec.Default {
		out.ClassifierVersion, out.ClassifierTrainedAt = cls.Model.Version, &cls.TrainedAt
		for _, t := range cls.Model.Tiers {
			byTier[t.Tier] = &ClassifierTierOut{Threshold: float64(t.Threshold), Precision: t.Metrics.Precision,
				Coverage: t.Metrics.Coverage, AUC: t.Metrics.AUC, Samples: t.Metrics.Samples}
		}
	}
	for i, t := range rep.Tiers {
		out.Tiers[i] = RouterReportTierOut{Name: t.Name, Model: rt.Spec.Tiers[i].Model, Rank: t.Rank, Served: t.Served,
			Decided: t.Decided, CostMicros: t.CostMicros, Judged: t.Judged, Wins: t.Wins, Ties: t.Ties, Losses: t.Losses,
			Unjudged: t.Unjudged, Quality: t.Quality, QualityLow: t.Low, QualityHigh: t.High, MeetsFloor: t.MeetsFloor,
			Demoted: rt.IsDemoted(t.Name), Classifier: byTier[t.Name], JudgeLeans: t.JudgeLeans, FlipRate: t.FlipRate,
			JudgeBoth: t.JudgeBoth}
	}
	for _, f := range rep.Families {
		out.Families = append(out.Families, RouterReportFamilyOut{Family: f.Family, Tier: f.Tier, Judged: f.Judged,
			Quality: f.Quality, QualityLow: f.Low, MeetsFloor: f.MeetsFloor})
	}
	return out, nil
}

// TrainRouterOut says a training was queued.
type TrainRouterOut struct {
	Queued bool `json:"queued"`
}

// TrainRouter queues training the router's classifier now, instead of at
// the next hourly run.
func (h *Handlers) TrainRouter(ctx *muzak.Context, in RouterPath) (TrainRouterOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	row, err := h.router(ctx, in.Router)
	if err != nil {
		return TrainRouterOut{}, err
	}
	if h.RouterDeps.Jobs == nil {
		return TrainRouterOut{}, muzak.ServiceUnavailable("jobs aren't available")
	}
	if err := h.RouterDeps.Jobs.Enqueue(ctx.Context(), training.RouterArgs{Org: pa.OrgID(), Router: row.ID}, nil); err != nil {
		return TrainRouterOut{}, err
	}
	h.audited(ctx, "router.training_requested", "router", in.Router, map[string]any{})
	return TrainRouterOut{Queued: true}, nil
}
