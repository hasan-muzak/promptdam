package routers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/notify"
	"github.com/hasan-muzak/keel-core/pkg/webhooks"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/modelrouter"
	"github.com/hasan-muzak/promptdam/server/features/activity"
	"github.com/hasan-muzak/promptdam/server/features/classifier"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

// IDPrefix starts model router IDs at the API: rtr_….
const IDPrefix = "rtr"

// Windows the watcher reads.
const (
	// Evidence is how far back a shadow router's report is read for
	// router.ready and for activation.
	Evidence = 14 * 24 * time.Hour
	// CanaryWindow is how far back an active router's served calls are
	// read for demotions.
	CanaryWindow = 7 * 24 * time.Hour
)

// Webhook events.
var (
	EventReady = webhooks.EventType{Name: "router.ready",
		Description: "A model router in shadow mode met its quality floor on every tier it would use: it can be made active."}
	EventTierDemoted = webhooks.EventType{Name: "router.tier_demoted",
		Description: "An active model router's tier fell below its quality floor on the calls it served; its calls go to the next " +
			"stronger tier until the project restores it."}
)

// WatchConfig configures a Watcher.
type WatchConfig struct {
	Ent      *pdent.Client
	Events   *events.DB
	Notifier *notify.Notifier
	Webhooks *webhooks.Service
	// Activity keeps each announcement in the project's feed (nil: none).
	Activity *activity.Service
	// AppURL builds the dashboard's links.
	AppURL string
	// Changed runs after a router's demotions change.
	Changed func(ctx context.Context, project uuid.UUID)
	Now     func() time.Time
}

// Watcher announces shadow routers whose evidence turns ready, and demotes
// active routers' tiers whose quality on the calls they served falls below
// the floor: the canary. It runs hourly.
type Watcher struct{ cfg WatchConfig }

// NewWatcher returns a Watcher.
func NewWatcher(cfg WatchConfig) *Watcher {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Changed == nil {
		cfg.Changed = func(context.Context, uuid.UUID) {}
	}
	return &Watcher{cfg: cfg}
}

// RouterPayload is a router in webhooks.
type RouterPayload struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Name    string `json:"name"`
	// Tier is the demoted tier (router.tier_demoted).
	Tier    string  `json:"tier,omitempty"`
	Judged  int     `json:"judged,omitempty"`
	Quality float64 `json:"quality,omitempty"`
	Floor   float64 `json:"quality_floor"`
	URL     string  `json:"url"`
}

// Check looks at every shadow router not yet announced and every active
// router.
func (w *Watcher) Check(ctx context.Context) error {
	if w.cfg.Events == nil {
		return nil
	}
	all := entx.SkipTenant(ctx)
	rows, err := w.cfg.Ent.ModelRouter.Query().Where(modelrouter.Or(
		modelrouter.And(modelrouter.StatusEQ(modelrouter.StatusShadow), modelrouter.ReadyAtIsNil()),
		modelrouter.StatusEQ(modelrouter.StatusActive),
	)).All(all)
	if err != nil {
		return fmt.Errorf("routers: watching: %w", err)
	}
	var errs []error
	for _, r := range rows {
		rctx := entx.WithOrg(ctx, r.OrgID)
		if r.Status == modelrouter.StatusShadow {
			errs = append(errs, w.announce(rctx, r))
		} else {
			errs = append(errs, w.canary(rctx, r))
		}
	}
	return errors.Join(errs...)
}

func (w *Watcher) url(r *pdent.ModelRouter) string {
	return strings.TrimRight(w.cfg.AppURL, "/") + "/projects/" + ids.Encode(ids.Project, r.ProjectID) + "/routers/" +
		ids.Encode(IDPrefix, r.ID)
}

// announce marks a shadow router ready once its report is, and says so.
func (w *Watcher) announce(ctx context.Context, r *pdent.ModelRouter) error {
	now := w.cfg.Now()
	rep, err := BuildReport(ctx, w.cfg.Events, r.ProjectID, Of(r), now.Add(-Evidence), now, nil)
	if err != nil || !rep.Ready {
		return err
	}
	if err := w.cfg.Ent.ModelRouter.UpdateOne(r).SetReadyAt(now.UTC()).Exec(ctx); err != nil {
		return err
	}
	p := RouterPayload{ID: ids.Encode(IDPrefix, r.ID), Project: ids.Encode(ids.Project, r.ProjectID), Name: r.Name,
		Floor: r.QualityFloor, URL: w.url(r)}
	w.tell(ctx, r.OrgID, r.ProjectID, EventReady, p, notify.Alert{
		Title:    fmt.Sprintf("Model router %s is ready to serve", r.Name),
		Body:     fmt.Sprintf("In shadow mode, every tier it would use met its %.0f%% quality floor on your calls.", 100*r.QualityFloor),
		URL:      p.URL,
		Severity: "info",
	})
	return nil
}

// canary demotes an active router's cheaper tiers whose quality on the
// calls they served is, with 95% confidence, below the floor.
func (w *Watcher) canary(ctx context.Context, r *pdent.ModelRouter) error {
	var rows []struct {
		Tier   string `ch:"candidate_tier"`
		Judged uint64 `ch:"judged"`
		Held   uint64 `ch:"held"`
	}
	if err := w.cfg.Events.Select(ctx, &rows, events.Project(r.ProjectID), `SELECT candidate_tier,
		countIf(verdict != '') AS judged, countIf(verdict IN ('win', 'tie')) AS held
		FROM router_evals WHERE {tenant} AND router = ? AND baseline_tier = ? AND served_tier = candidate_tier
		AND ts >= fromUnixTimestamp64Milli(?) GROUP BY candidate_tier`, r.Name, r.DefaultTier,
		w.cfg.Now().Add(-CanaryWindow).UnixMilli()); err != nil {
		return fmt.Errorf("routers: canary: %w", err)
	}
	rt := Of(r)
	def := rt.Spec.Index(r.DefaultTier)
	var demoted []tiers.Demotion
	for _, row := range rows {
		i := rt.Spec.Index(row.Tier)
		if i < 0 || i >= def || rt.IsDemoted(row.Tier) || row.Judged < MinJudged {
			continue
		}
		if _, high := classifier.Wilson(int(row.Held), int(row.Judged)); high < r.QualityFloor {
			demoted = append(demoted, tiers.Demotion{Tier: row.Tier, At: w.cfg.Now().UTC(), Judged: int(row.Judged),
				Right: int(row.Held), High: high})
		}
	}
	if len(demoted) == 0 {
		return nil
	}
	if err := w.cfg.Ent.ModelRouter.UpdateOne(r).SetDemoted(append(rt.Demoted, demoted...)).Exec(ctx); err != nil {
		return err
	}
	w.cfg.Changed(ctx, r.ProjectID)
	for _, d := range demoted {
		quality := float64(d.Right) / float64(d.Judged)
		p := RouterPayload{ID: ids.Encode(IDPrefix, r.ID), Project: ids.Encode(ids.Project, r.ProjectID), Name: r.Name, Tier: d.Tier,
			Judged: d.Judged, Quality: quality, Floor: r.QualityFloor, URL: w.url(r)}
		slog.Warn("routers: a tier was demoted", "router", r.ID, "tier", d.Tier, "judged", d.Judged, "quality", quality)
		w.tell(ctx, r.OrgID, r.ProjectID, EventTierDemoted, p, notify.Alert{
			Title: fmt.Sprintf("Model router %s demoted its %s tier", r.Name, d.Tier),
			Body: fmt.Sprintf("Its answers held up on %.0f%% of %d judged calls, under the %.0f%% floor. Its calls go to the next "+
				"stronger tier until you restore it.", 100*quality, d.Judged, 100*r.QualityFloor),
			URL:      p.URL,
			Severity: "warning",
		})
	}
	return nil
}

// tell publishes an event and alerts the organization's channels.
func (w *Watcher) tell(ctx context.Context, org, project uuid.UUID, event webhooks.EventType, p RouterPayload, a notify.Alert) {
	data := map[string]any{"router": p.Name}
	if p.Tier != "" {
		data["tier"], data["judged"], data["quality"], data["floor"] = p.Tier, p.Judged, p.Quality, p.Floor
	}
	w.cfg.Activity.Record(ctx, activity.Entry{Org: org, Project: project, Kind: event.Name, Severity: a.Severity, Title: a.Title,
		Body: a.Body, Subject: p.ID, URL: a.URL, Data: data})
	if w.cfg.Webhooks != nil {
		if err := w.cfg.Webhooks.Publish(ctx, org, event.Name, map[string]any{"router": p}); err != nil {
			slog.Warn("routers: publishing a webhook", "event", event.Name, "err", err)
		}
	}
	if w.cfg.Notifier != nil {
		if err := w.cfg.Notifier.Alert(ctx, org, a); err != nil {
			slog.Warn("routers: alerting", "event", event.Name, "err", err)
		}
	}
}

// WatchArgs runs the watcher (hourly).
type WatchArgs struct{}

// Kind implements river.JobArgs.
func (WatchArgs) Kind() string { return "promptdam_routers_watch" }

type watchWorker struct {
	river.WorkerDefaults[WatchArgs]
	w *Watcher
}

func (w *watchWorker) Work(ctx context.Context, _ *river.Job[WatchArgs]) error { return w.w.Check(ctx) }

// RegisterWorkers registers the watcher, run hourly.
func RegisterWorkers(j *jobs.Jobs, w *Watcher) {
	jobs.Register(j, &watchWorker{w: w})
	j.Every("promptdam-routers-watch", time.Hour, func() river.JobArgs { return WatchArgs{} })
}
