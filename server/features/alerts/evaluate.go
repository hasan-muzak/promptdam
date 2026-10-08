package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/money"
	"github.com/hasan-muzak/keel-core/pkg/notify"
	"github.com/hasan-muzak/keel-core/pkg/webhooks"

	"github.com/hasan-muzak/promptdam/server/catalog"
	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/alertrule"
	"github.com/hasan-muzak/promptdam/server/features/activity"
)

// Webhook events.
var (
	EventFired = webhooks.EventType{Name: "alert.fired",
		Description: "An alert rule's condition holds: spend spiking, provider errors, a provider down, slow answers. " +
			"Sent again after the rule's cooldown while it still holds."}
	EventResolved = webhooks.EventType{Name: "alert.resolved",
		Description: "An alert rule that fired no longer holds."}
)

// settle is how long calls have to reach the request log before a window
// that ends with them is read: it is written in batches.
const settle = 15 * time.Second

// baselineHours is the spend spike's usual hour: the average of the week
// before the last hour.
const baselineHours = 7 * 24

// EvalConfig configures an Evaluator.
type EvalConfig struct {
	Ent      *pdent.Client
	Events   *events.DB
	Notifier *notify.Notifier
	Webhooks *webhooks.Service
	Activity *activity.Service
	// Entitlements says whose plan has alert rules: the others' rules are
	// kept, not evaluated (nil evaluates all).
	Entitlements func(ctx context.Context, org uuid.UUID) (billing.Entitlements, error)
	AppURL       string
	Now          func() time.Time
}

// Evaluator evaluates enabled rules (every minute, in the worker).
type Evaluator struct{ cfg EvalConfig }

// NewEvaluator returns an Evaluator.
func NewEvaluator(cfg EvalConfig) *Evaluator {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Evaluator{cfg: cfg}
}

// scope is the calls a measure counts: a project's, one provider's, one
// model's, or one model at one provider ("" for any).
type scope struct {
	Project         uuid.UUID
	Provider, Model string
}

// measure is what a window's calls did.
type measure struct {
	Project  uuid.UUID `ch:"project_id"`
	Provider string    `ch:"provider"`
	Model    string    `ch:"served_model"`
	Calls    uint64    `ch:"calls"`
	Failed   uint64    `ch:"failed"`
	Cost     int64     `ch:"cost"`
	P95      float64   `ch:"p95"`
}

// groupingSets give, in one pass, each project's totals, per provider, per
// model and per model at each provider. A total's provider and model come
// back empty: calls that reached no provider are left out, so empty means
// all.
const groupingSets = `GROUP BY GROUPING SETS ((project_id), (project_id, provider), (project_id, served_model),
	(project_id, provider, served_model))`

// Evaluate reads every enabled rule's window and fires, repeats or
// resolves it.
func (e *Evaluator) Evaluate(ctx context.Context) error {
	if e.cfg.Events == nil {
		return nil
	}
	all := entx.SkipTenant(ctx)
	rules, err := e.cfg.Ent.AlertRule.Query().Where(alertrule.Enabled(true)).All(all)
	if err != nil {
		return fmt.Errorf("alerts: reading rules: %w", err)
	}
	rules = e.planned(ctx, rules)
	if len(rules) == 0 {
		return nil
	}
	end := e.cfg.Now().Add(-settle)
	windows := map[int][]uuid.UUID{}
	var spiking []uuid.UUID
	for _, r := range rules {
		if !slices.Contains(windows[r.WindowMinutes], r.ProjectID) {
			windows[r.WindowMinutes] = append(windows[r.WindowMinutes], r.ProjectID)
		}
		if string(r.Kind) == SpendSpike && !slices.Contains(spiking, r.ProjectID) {
			spiking = append(spiking, r.ProjectID)
		}
	}
	measured := map[int]map[scope]measure{}
	for minutes, projects := range windows {
		var rows []measure
		err := e.cfg.Events.Unscoped().Select(ctx, &rows, `SELECT project_id, provider, served_model, count() AS calls,
			countIf(startsWith(error_code, 'provider_')) AS failed, sum(cost_micros) AS cost,
			toFloat64(quantileTDigest(0.95)(latency_ms)) AS p95
			FROM requests WHERE project_id IN ? AND ts >= fromUnixTimestamp64Milli(?) AND ts < fromUnixTimestamp64Milli(?)
			AND provider != '' AND served_model != '' `+groupingSets,
			projects, end.Add(-time.Duration(minutes)*time.Minute).UnixMilli(), end.UnixMilli())
		if err != nil {
			return fmt.Errorf("alerts: reading %d minutes of calls: %w", minutes, err)
		}
		measured[minutes] = index(rows)
	}
	usual := map[scope]measure{}
	if len(spiking) > 0 {
		var rows []measure
		err := e.cfg.Events.Unscoped().Select(ctx, &rows, `SELECT project_id, provider, served_model, sum(requests) AS calls,
			sum(cost_micros) AS cost FROM requests_hourly WHERE project_id IN ?
			AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?)) AND hour < toStartOfHour(fromUnixTimestamp64Milli(?))
			AND provider != '' AND served_model != '' `+groupingSets,
			spiking, end.Add(-(baselineHours+1)*time.Hour).UnixMilli(), end.Add(-time.Hour).UnixMilli())
		if err != nil {
			return fmt.Errorf("alerts: reading the usual spend: %w", err)
		}
		usual = index(rows)
	}
	for _, r := range rules {
		v := e.verdict(r, measured[r.WindowMinutes], usual)
		if err := e.apply(ctx, r, v); err != nil {
			slog.Warn("alerts: a rule's evaluation wasn't kept", "rule", r.ID, "err", err)
		}
	}
	return nil
}

func index(rows []measure) map[scope]measure {
	out := make(map[scope]measure, len(rows))
	for _, m := range rows {
		out[scope{m.Project, m.Provider, m.Model}] = m
	}
	return out
}

// planned keeps the rules of organizations whose plan has alert rules.
func (e *Evaluator) planned(ctx context.Context, rules []*pdent.AlertRule) []*pdent.AlertRule {
	if e.cfg.Entitlements == nil {
		return rules
	}
	has := map[uuid.UUID]bool{}
	return slices.DeleteFunc(rules, func(r *pdent.AlertRule) bool {
		ok, seen := has[r.OrgID]
		if !seen {
			ent, err := e.cfg.Entitlements(ctx, r.OrgID)
			ok = err == nil && ent.Has(catalog.Alerts)
			has[r.OrgID] = ok
		}
		return !ok
	})
}

// verdict is what a rule's window says.
type verdict struct {
	holds bool
	// value is what was measured, in the threshold's unit.
	value float64
	// on are the providers that look down (provider_down).
	on []string
	// detail says it in words, for alerts.
	detail string
}

func (e *Evaluator) verdict(r *pdent.AlertRule, m map[scope]measure, usual map[scope]measure) verdict {
	sc := scope{r.ProjectID, r.Provider, r.Model}
	got := m[sc]
	share := func(x measure) float64 {
		if x.Calls == 0 {
			return 0
		}
		return float64(x.Failed) / float64(x.Calls)
	}
	enough := got.Calls >= uint64(r.MinCalls)
	switch string(r.Kind) {
	case SpendSpike:
		hour := float64(usual[sc].Cost) / baselineHours
		v := verdict{holds: got.Cost >= r.MinMicros && float64(got.Cost) >= r.Threshold*hour}
		if hour > 0 {
			v.value = math.Min(float64(got.Cost)/hour, 1000)
		}
		v.detail = fmt.Sprintf("%s in the last hour; usually %s an hour (the week before).", money.Micros(got.Cost),
			money.Micros(int64(hour)))
		return v
	case ErrorRate:
		v := verdict{holds: enough && share(got) >= r.Threshold, value: share(got)}
		v.detail = fmt.Sprintf("%d of %d calls failed at the provider (rate limits, server errors, timeouts).", got.Failed, got.Calls)
		return v
	case Latency:
		v := verdict{holds: enough && got.P95 >= r.Threshold, value: got.P95}
		v.detail = fmt.Sprintf("p95 %s over %d calls.", seconds(got.P95), got.Calls)
		return v
	}
	// provider_down: the rule's provider, else each provider of the
	// project.
	var v verdict
	var parts []string
	for s, x := range m {
		if s.Project != r.ProjectID || s.Provider == "" || s.Model != r.Model || r.Provider != "" && s.Provider != r.Provider {
			continue
		}
		v.value = math.Max(v.value, share(x))
		if x.Calls >= uint64(r.MinCalls) && share(x) >= r.Threshold {
			v.on = append(v.on, s.Provider)
			parts = append(parts, fmt.Sprintf("%s failed %d of %d calls", s.Provider, x.Failed, x.Calls))
		}
	}
	slices.Sort(v.on)
	slices.Sort(parts)
	v.holds = len(v.on) > 0
	v.detail = strings.Join(parts, "; ") + ". Fallbacks take what they can."
	return v
}

// apply moves a rule to what its verdict says: fired on crossing, fired
// again after its cooldown or when another provider goes down, resolved
// when it no longer holds.
func (e *Evaluator) apply(ctx context.Context, r *pdent.AlertRule, v verdict) error {
	now := e.cfg.Now()
	u := e.cfg.Ent.AlertRule.UpdateOne(r).SetEvaluatedAt(now).SetLastValue(v.value)
	newlyDown := slices.ContainsFunc(v.on, func(p string) bool { return !slices.Contains(r.FiringOn, p) })
	switch {
	case v.holds && (!r.Firing || newlyDown || r.FiredAt == nil ||
		now.Sub(*r.FiredAt) >= time.Duration(r.CooldownMinutes)*time.Minute):
		u.SetFiring(true).SetFiredAt(now).SetFiringOn(orEmpty(v.on))
		e.tell(ctx, r, v, EventFired, r.Firing)
	case v.holds:
		u.SetFiringOn(orEmpty(v.on))
	case r.Firing:
		u.SetFiring(false).SetResolvedAt(now).SetFiringOn([]string{})
		e.tell(ctx, r, v, EventResolved, false)
	}
	return u.Exec(entx.WithOrg(ctx, r.OrgID))
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Payload is an alert rule's firing or resolution in webhooks.
type Payload struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Condition string    `json:"condition"`
	Value     float64   `json:"value" doc:"What was measured, in the threshold's unit"`
	Threshold float64   `json:"threshold"`
	Window    int       `json:"window_minutes"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	Down      []string  `json:"down,omitempty"`
	Repeat    bool      `json:"repeat" doc:"Fired again: it held through its cooldown"`
	Detail    string    `json:"detail"`
	At        time.Time `json:"at"`
	URL       string    `json:"url"`
}

// tell alerts the rule's channels, publishes its webhook and keeps it in
// the project's activity.
func (e *Evaluator) tell(ctx context.Context, r *pdent.AlertRule, v verdict, event webhooks.EventType, repeat bool) {
	id := ids.Encode(IDPrefix, r.ID)
	url := strings.TrimRight(e.cfg.AppURL, "/") + "/projects/" + ids.Encode(ids.Project, r.ProjectID) + "/alerts/" + id
	cond := describe(r)
	title, severity := "Alert "+r.Name+": "+cond, activity.Warning
	if string(r.Kind) == ProviderDown {
		title = "Alert " + r.Name + ": " + strings.Join(v.on, ", ") + " looks down"
		severity = activity.Critical
	}
	if event == EventResolved {
		title, severity = "Resolved: "+r.Name+", "+cond+", no longer holds", activity.Info
	}
	if e.cfg.Notifier != nil {
		var channels []uuid.UUID
		for _, c := range r.ChannelIds {
			if id, err := uuid.Parse(c); err == nil {
				channels = append(channels, id)
			}
		}
		if err := e.cfg.Notifier.AlertTo(ctx, r.OrgID, channels, notify.Alert{Title: title, Body: v.detail, URL: url,
			Severity: severity}); err != nil {
			slog.Warn("alerts: alerting", "rule", r.ID, "err", err)
		}
	}
	p := Payload{ID: id, Project: ids.Encode(ids.Project, r.ProjectID), Name: r.Name, Kind: string(r.Kind), Condition: cond,
		Value: v.value, Threshold: r.Threshold, Window: r.WindowMinutes, Provider: r.Provider, Model: r.Model, Down: v.on,
		Repeat: repeat, Detail: v.detail, At: e.cfg.Now().UTC(), URL: url}
	if e.cfg.Webhooks != nil {
		if err := e.cfg.Webhooks.Publish(ctx, r.OrgID, event.Name, map[string]any{"alert": p}); err != nil {
			slog.Warn("alerts: publishing a webhook", "rule", r.ID, "err", err)
		}
	}
	e.cfg.Activity.Record(ctx, activity.Entry{Org: r.OrgID, Project: r.ProjectID, Kind: event.Name, Severity: severity,
		Title: title, Body: v.detail, Subject: id, URL: url, Data: map[string]any{"kind": p.Kind, "value": p.Value,
			"threshold": p.Threshold, "window_minutes": p.Window, "down": orEmpty(v.on), "repeat": repeat}})
}

// EvaluateArgs evaluates the rules (every minute).
type EvaluateArgs struct{}

// Kind implements river.JobArgs.
func (EvaluateArgs) Kind() string { return "promptdam_alerts_evaluate" }

type evaluateWorker struct {
	river.WorkerDefaults[EvaluateArgs]
	e *Evaluator
}

func (w *evaluateWorker) Work(ctx context.Context, _ *river.Job[EvaluateArgs]) error {
	return w.e.Evaluate(ctx)
}

// RegisterWorkers evaluates the rules every minute.
func RegisterWorkers(j *jobs.Jobs, e *Evaluator) {
	jobs.Register(j, &evaluateWorker{e: e})
	j.Every("promptdam-alerts-evaluate", time.Minute, func() river.JobArgs { return EvaluateArgs{} })
}
