package handlers

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/money"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/catalog"
	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/features/activity"
	"github.com/hasan-muzak/promptdam/server/features/alerts"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

// OverviewIn asks for a project's overview.
type OverviewIn struct {
	platform.ProjectPath
	From string `query:"from" doc:"Start (RFC 3339), inclusive; default the start of this month (UTC)"`
	To   string `query:"to" doc:"End (RFC 3339), exclusive; default now"`
}

// OverviewDayOut is a day's spend, and what it would have cost.
type OverviewDayOut struct {
	Day                  time.Time `json:"day"`
	CostMicros           int64     `json:"cost_micros"`
	CounterfactualMicros int64     `json:"counterfactual_micros" doc:"The models asked for at list price, without any cache"`
}

// TopOut is one of the busiest models, keys or families.
type TopOut struct {
	ID         string `json:"id" doc:"The model, the key (key_…) or the family"`
	Label      string `json:"label" doc:"A family's name, when it has one"`
	Requests   uint64 `json:"requests"`
	CostMicros int64  `json:"cost_micros"`
}

// AttentionOut is something the project should look at.
type AttentionOut struct {
	Kind     string `json:"kind" doc:"budget, router_ready, tier_demoted, router_watching, provider_check, alert_firing or missing_prices"`
	Severity string `json:"severity" doc:"critical, warning or info"`
	Title    string `json:"title"`
	Subject  string `json:"subject" doc:"What it is about: bdg_…, rtr_…, prv_…, alr_…; empty for missing prices"`
}

// OverviewOut is a project at a glance.
type OverviewOut struct {
	From                 time.Time        `json:"from"`
	To                   time.Time        `json:"to"`
	Requests             uint64           `json:"requests"`
	Errors               uint64           `json:"errors"`
	CostMicros           int64            `json:"cost_micros"`
	PreviousCostMicros   int64            `json:"previous_cost_micros" doc:"The same length of time before from"`
	CounterfactualMicros int64            `json:"counterfactual_micros"`
	SavedMicros          int64            `json:"saved_micros"`
	LatencyP95Ms         float64          `json:"latency_p95_ms"`
	PromptCacheShare     float64          `json:"prompt_cache_share" doc:"The share of input tokens the providers' prompt caches served"`
	ResponseCacheHits    uint64           `json:"response_cache_hits"`
	FirewallCalls        uint64           `json:"firewall_calls" doc:"Calls the firewall found something in, or a policy decided (ranges of 90 days at most)"`
	FirewallBlocked      uint64           `json:"firewall_blocked"`
	Days                 []OverviewDayOut `json:"days"`
	TopModels            []TopOut         `json:"top_models"`
	TopKeys              []TopOut         `json:"top_keys"`
	TopFamilies          []TopOut         `json:"top_families"`
	Attention            []AttentionOut   `json:"attention" doc:"Most urgent first"`
	Activity             []ActivityOut    `json:"activity" doc:"The five most recent"`
}

// GetOverview returns a project at a glance: spend against the period
// before and against what it would have cost, latency, caching, the
// firewall, where the money goes, what needs attention and what happened.
func (h *Handlers) GetOverview(ctx *muzak.Context, in OverviewIn) (OverviewOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	project := pa.Project.ID
	from, to, err := monthToDate(in.From, in.To)
	if err != nil {
		return OverviewOut{}, err
	}
	out := OverviewOut{From: from, To: to, Days: []OverviewDayOut{}, Attention: []AttentionOut{}, Activity: []ActivityOut{}}
	c := ctx.Context()
	var (
		spend, previous, byModel, byKey analytics.Spend
		saved                           analytics.SavingsReport
		latency                         []analytics.Latency
		input, cacheRead                uint64
		fs                              []families.Family
		fw                              firewall.Stats
		recent                          []*pdent.Activity
		attention                       []AttentionOut
	)
	g, gc := errgroup.WithContext(c)
	g.Go(func() (err error) { spend, err = h.Analytics.Spend(gc, project, from, to, "", "day"); return })
	g.Go(func() (err error) {
		previous, err = h.Analytics.Spend(gc, project, from.Add(-to.Sub(from)), from, "", "day")
		return
	})
	g.Go(func() (err error) { byModel, err = h.Analytics.Spend(gc, project, from, to, "model", "day"); return })
	g.Go(func() (err error) { byKey, err = h.Analytics.Spend(gc, project, from, to, "key", "day"); return })
	g.Go(func() (err error) { saved, err = h.Analytics.Savings(gc, project, from, to); return })
	g.Go(func() (err error) { latency, err = h.Analytics.Latency(gc, project, from, to, ""); return })
	g.Go(func() (err error) { input, cacheRead, err = h.Analytics.Tokens(gc, project, from, to); return })
	g.Go(func() (err error) { fs, err = h.Families.Top(gc, project, from, to, 5); return })
	if to.Sub(from) <= firewall.MaxStatsRange {
		g.Go(func() (err error) { fw, err = firewall.ReadStats(gc, h.RouterDeps.Events, project, from, to); return })
	}
	g.Go(func() (err error) {
		recent, _, err = h.Activity.List(gc, project, activity.Filter{}, uuid.Nil, 5)
		return
	})
	g.Go(func() (err error) { attention, err = h.attention(ctx, project); return })
	if err := g.Wait(); err != nil {
		return OverviewOut{}, analyticsFail(err)
	}

	out.Requests, out.Errors, out.CostMicros = spend.Total.Requests, spend.Total.Errors, spend.Total.CostMicros
	out.PreviousCostMicros = previous.Total.CostMicros
	out.CounterfactualMicros, out.SavedMicros = saved.Total.Counterfactual, saved.Total.Savings
	for _, r := range saved.ByReason {
		if r.Key == "response_cache" {
			out.ResponseCacheHits = r.Requests
		}
	}
	for _, p := range saved.Series {
		out.Days = append(out.Days, OverviewDayOut{Day: p.T.UTC(), CostMicros: p.Cost, CounterfactualMicros: p.Cost + p.Savings})
	}
	if len(latency) > 0 && len(latency[0].Total) > 1 {
		out.LatencyP95Ms = float64(latency[0].Total[1])
	}
	if input > 0 {
		out.PromptCacheShare = float64(cacheRead) / float64(input)
	}
	out.FirewallCalls, out.FirewallBlocked = fw.Calls, fw.Blocked
	out.TopModels = top(byModel.Groups, func(k string) string { return k })
	out.TopKeys = top(byKey.Groups, func(k string) string {
		if id, err := uuid.Parse(k); err == nil {
			return ids.Encode(ids.APIKey, id)
		}
		return k
	})
	out.TopFamilies = make([]TopOut, 0, len(fs))
	for _, f := range fs {
		out.TopFamilies = append(out.TopFamilies, TopOut{ID: f.ID, Label: f.Label, Requests: f.Requests, CostMicros: f.CostMicros})
	}
	out.Attention = attention
	for _, a := range recent {
		out.Activity = append(out.Activity, ActivityOut{ID: ids.Encode(activity.IDPrefix, a.ID), Time: a.CreatedAt.UTC(),
			Kind: a.Kind, Severity: string(a.Severity), Title: a.Title, Body: a.Body, Subject: a.Subject, URL: a.URL, Data: a.Data})
	}
	return out, nil
}

// monthToDate reads a range, by default this month (UTC) to now.
func monthToDate(rawFrom, rawTo string) (from, to time.Time, err error) {
	to = time.Now().UTC()
	if rawTo != "" {
		if to, err = time.Parse(time.RFC3339, rawTo); err != nil {
			return from, to, muzak.UnprocessableEntity("to is not an RFC 3339 time").WithCode(CodeInvalidQuery)
		}
		to = to.UTC()
	}
	from = time.Date(to.Year(), to.Month(), 1, 0, 0, 0, 0, time.UTC)
	if rawFrom != "" {
		if from, err = time.Parse(time.RFC3339, rawFrom); err != nil {
			return from, to, muzak.UnprocessableEntity("from is not an RFC 3339 time").WithCode(CodeInvalidQuery)
		}
		from = from.UTC()
	}
	if !from.Before(to) || to.Sub(from) > analytics.MaxRange {
		return from, to, muzak.UnprocessableEntity("the time range must start before it ends and cover at most 400 days").
			WithCode(CodeInvalidQuery)
	}
	return from, to, nil
}

// ProjectsUsageIn asks for an organization's projects' usage.
type ProjectsUsageIn struct {
	platform.OrgPath
	From string `query:"from" doc:"Start (RFC 3339), inclusive; default the start of this month (UTC)"`
	To   string `query:"to" doc:"End (RFC 3339), exclusive; default now"`
}

// ProjectUsageOut is one project's calls, spend and savings.
type ProjectUsageOut struct {
	Project     string `json:"project" doc:"Project ID (prj_…)"`
	Name        string `json:"name"`
	Requests    uint64 `json:"requests"`
	Errors      uint64 `json:"errors"`
	CostMicros  int64  `json:"cost_micros"`
	SavedMicros int64  `json:"saved_micros"`
}

// ProjectsUsageOut is each project's usage over a range.
type ProjectsUsageOut struct {
	From time.Time         `json:"from"`
	To   time.Time         `json:"to"`
	Data []ProjectUsageOut `json:"data" doc:"By spend; projects without calls last"`
}

// GetProjectsUsage returns each of an organization's projects' calls,
// spend and savings over a range.
func (h *Handlers) GetProjectsUsage(ctx *muzak.Context, in ProjectsUsageIn) (ProjectsUsageOut, error) {
	a := muzak.From[tenancy.Access](ctx)
	from, to, err := monthToDate(in.From, in.To)
	if err != nil {
		return ProjectsUsageOut{}, err
	}
	ps, err := h.Tenancy.Projects(ctx.Context(), a.OrgID())
	if err != nil {
		return ProjectsUsageOut{}, err
	}
	pids := make([]uuid.UUID, len(ps))
	for i, p := range ps {
		pids[i] = p.ID
	}
	used, err := h.Analytics.Projects(ctx.Context(), pids, from, to)
	if err != nil {
		return ProjectsUsageOut{}, analyticsFail(err)
	}
	names := make(map[uuid.UUID]string, len(ps))
	for _, p := range ps {
		names[p.ID] = p.Name
	}
	out := ProjectsUsageOut{From: from, To: to, Data: make([]ProjectUsageOut, 0, len(ps))}
	seen := make(map[uuid.UUID]bool, len(used))
	for _, u := range used {
		seen[u.Project] = true
		out.Data = append(out.Data, ProjectUsageOut{Project: ids.Encode(ids.Project, u.Project), Name: names[u.Project],
			Requests: u.Requests, Errors: u.Errors, CostMicros: u.Cost, SavedMicros: u.Savings})
	}
	for _, p := range ps {
		if !seen[p.ID] {
			out.Data = append(out.Data, ProjectUsageOut{Project: ids.Encode(ids.Project, p.ID), Name: p.Name})
		}
	}
	return out, nil
}

// top keeps the five groups with the most spend.
func top(gs []analytics.Group, id func(string) string) []TopOut {
	out := make([]TopOut, 0, 5)
	for _, g := range gs[:min(len(gs), 5)] {
		out = append(out, TopOut{ID: id(g.Key), Requests: g.Requests, CostMicros: g.CostMicros})
	}
	return out
}

// attention lists what the project should look at, most urgent first:
// budgets past a threshold, routers ready, demoted or kept watching by the
// plan, providers whose check failed, alert rules firing, models without a
// price.
func (h *Handlers) attention(ctx *muzak.Context, project uuid.UUID) ([]AttentionOut, error) {
	c := ctx.Context()
	out := []AttentionOut{}
	bs, err := h.Budgets.List(c, project)
	if err != nil {
		return nil, err
	}
	for _, b := range bs {
		if !b.Enabled {
			continue
		}
		st, err := h.Budgets.Status(c, b)
		if err != nil {
			return nil, err
		}
		first := 80
		if len(b.Thresholds) > 0 {
			first = slices.Min(b.Thresholds)
		}
		if st.Percent < float64(first) {
			continue
		}
		severity := "warning"
		if st.Percent >= 100 {
			severity = "critical"
		}
		out = append(out, AttentionOut{Kind: "budget", Severity: severity, Subject: ids.Encode(budgets.IDPrefix, b.ID),
			Title: fmt.Sprintf("Budget %s is at %.0f%% of %s per %s", b.Name, st.Percent, money.Micros(b.LimitMicros), b.Window)})
	}
	rs, err := h.Routers.List(c, project)
	if err != nil {
		return nil, err
	}
	activeOK := h.planHas(ctx, catalog.Routers)
	for _, r := range rs {
		id := ids.Encode(routers.IDPrefix, r.ID)
		switch {
		case string(r.Status) == tiers.Shadow && r.ReadyAt != nil:
			out = append(out, AttentionOut{Kind: "router_ready", Severity: "info", Subject: id,
				Title: "Router " + r.Name + " is ready to serve: its cheaper tiers hold up"})
		case string(r.Status) == tiers.Active && !activeOK:
			out = append(out, AttentionOut{Kind: "router_watching", Severity: "warning", Subject: id,
				Title: "Router " + r.Name + " only watches: your plan doesn't include active routers"})
		}
		for _, d := range r.Demoted {
			out = append(out, AttentionOut{Kind: "tier_demoted", Severity: "warning", Subject: id,
				Title: fmt.Sprintf("Router %s demoted its %s tier: below its quality floor", r.Name, d.Tier)})
		}
	}
	ps, err := h.Providers.List(c, project)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Enabled && p.CheckError != "" {
			out = append(out, AttentionOut{Kind: "provider_check", Severity: "warning", Subject: ids.Encode(ProviderPrefix, p.ID),
				Title: "Provider " + p.Name + "'s last check failed: " + p.CheckError})
		}
	}
	as, err := h.Alerts.List(c, project)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		if a.Firing {
			severity := "warning"
			if string(a.Kind) == alerts.ProviderDown {
				severity = "critical"
			}
			out = append(out, AttentionOut{Kind: "alert_firing", Severity: severity, Subject: ids.Encode(alerts.IDPrefix, a.ID),
				Title: "Alert " + a.Name + " is firing"})
		}
	}
	now := time.Now()
	missing, err := h.missingPrices(ctx, project, now.Add(-7*24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	if n := len(missing); n > 0 {
		title := fmt.Sprintf("%d models have no price: spend and savings leave their calls out", n)
		if n == 1 {
			title = "Model " + missing[0].Model + " has no price: spend and savings leave its calls out"
		}
		out = append(out, AttentionOut{Kind: "missing_prices", Severity: "info", Title: title})
	}
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	slices.SortStableFunc(out, func(a, b AttentionOut) int { return cmp.Compare(rank[a.Severity], rank[b.Severity]) })
	return out, nil
}
