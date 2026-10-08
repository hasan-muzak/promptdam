package firewall

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/kv"
	"github.com/hasan-muzak/keel-core/pkg/notify"
	"github.com/hasan-muzak/keel-core/pkg/webhooks"

	"github.com/hasan-muzak/promptdam/server/features/activity"
)

// EventBlocked is the webhook event of the calls the firewall refused.
var EventBlocked = webhooks.EventType{Name: "firewall.blocked",
	Description: "The firewall refused calls of a project: what it found in each (detectors, never the values) or the " +
		"policy that refused it. One a minute per project at most, listing up to 100 calls (more counts the rest); " +
		"the firewall's events list has them all."}

// Watch limits.
const (
	// maxPublished bounds the calls a project's firewall.blocked webhook
	// lists per run.
	maxPublished = 100
	// alertEvery is how often a project's blocks alert its channels.
	alertEvery = time.Hour
	// digestEvery is how often an organization gets the firewall's digest.
	digestEvery = 7 * 24 * time.Hour
	// settle is how long events have to reach ClickHouse before they are
	// read: the request log writes in batches.
	settle = 30 * time.Second
)

// WatchConfig configures a Watcher.
type WatchConfig struct {
	Events   *events.DB
	KV       *kv.Store
	Notifier *notify.Notifier
	Webhooks *webhooks.Service
	// Activity keeps each run's refusals in the project's feed (nil: none).
	Activity *activity.Service
	// AppURL builds the dashboard's links.
	AppURL string
	// ProjectName names a project in alerts (its ID when it fails).
	ProjectName func(ctx context.Context, project uuid.UUID) (string, error)
	Now         func() time.Time
}

// Watcher tells organizations what their firewalls did: a webhook per
// blocked call and an alert (hourly at most) as they happen, every
// minute, and a digest of each week.
type Watcher struct{ cfg WatchConfig }

// NewWatcher returns a Watcher.
func NewWatcher(cfg WatchConfig) *Watcher {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Watcher{cfg: cfg}
}

// BlockedPayload is a run's refused calls of a project in webhooks: one
// webhook, not one per call, so a burst of refusals doesn't become a
// burst of deliveries.
type BlockedPayload struct {
	Project string        `json:"project"`
	Calls   []BlockedCall `json:"calls"`
	// More counts the refused calls past those listed.
	More int    `json:"more"`
	URL  string `json:"url"`
}

// BlockedCall is a refused call.
type BlockedCall struct {
	Request   string    `json:"request"`
	Time      time.Time `json:"time"`
	BlockedBy string    `json:"blocked_by"`
	Policy    string    `json:"policy,omitempty"`
	Detectors []string  `json:"detectors"`
	Key       string    `json:"key"`
	Developer string    `json:"developer"`
	Route     string    `json:"route"`
	Model     string    `json:"model"`
}

type blockedCall struct {
	ProjectID uuid.UUID `ch:"project_id"`
	OrgID     uuid.UUID `ch:"org_id"`
	Trace     string    `ch:"trace"`
	TS        time.Time `ch:"at"`
	KeyID     uuid.UUID `ch:"key_id"`
	Developer string    `ch:"developer"`
	Route     string    `ch:"route"`
	Model     string    `ch:"requested_model"`
	BlockedBy string    `ch:"blocked_by"`
	Policy    string    `ch:"policy"`
	Detectors []string  `ch:"detectors"`
}

// maxRead bounds the blocked calls one run reads; the next run goes on
// from the last.
const maxRead = 10_000

// Check tells of the calls refused since the last run.
func (w *Watcher) Check(ctx context.Context) error {
	if w.cfg.Events == nil {
		return nil
	}
	c := w.cfg.KV.Client
	key := w.cfg.KV.Key("firewall", "watched")
	end := w.cfg.Now().Add(-settle)
	start := end.Add(-2 * time.Minute) // a first run looks back a little
	if ms, err := c.Do(ctx, c.B().Get().Key(key).Build()).AsInt64(); err == nil {
		start = time.UnixMilli(ms)
	}
	if !start.Before(end) {
		return nil
	}
	var calls []blockedCall
	err := w.cfg.Events.Unscoped().Select(ctx, &calls, `SELECT project_id, org_id, trace, min(ts) AS at, any(key_id) AS key_id,
		any(developer) AS developer, any(route) AS route, any(requested_model) AS requested_model, any(blocked_by) AS blocked_by,
		any(policy) AS policy, arraySort(groupUniqArrayIf(detector, detector != '')) AS detectors
		FROM `+EventsTable+` WHERE blocked AND ts >= fromUnixTimestamp64Milli(?) AND ts < fromUnixTimestamp64Milli(?)
		GROUP BY project_id, org_id, trace ORDER BY at, trace LIMIT ?`, start.UnixMilli(), end.UnixMilli(), maxRead)
	if err != nil {
		return fmt.Errorf("firewall: reading blocked calls: %w", err)
	}
	next := end
	if len(calls) == maxRead {
		// The last millisecond read may hold calls past the limit: it is
		// read again, its calls told twice rather than never.
		next = calls[len(calls)-1].TS
	}
	byProject := map[uuid.UUID][]blockedCall{}
	var order []uuid.UUID
	for _, b := range calls {
		if byProject[b.ProjectID] == nil {
			order = append(order, b.ProjectID)
		}
		byProject[b.ProjectID] = append(byProject[b.ProjectID], b)
	}
	for _, p := range order {
		w.tell(ctx, byProject[p])
	}
	return c.Do(ctx, c.B().Set().Key(key).Value(strconv.FormatInt(next.UnixMilli(), 10)).Build()).Error()
}

// url is a project's firewall page.
func (w *Watcher) url(project uuid.UUID) string {
	return strings.TrimRight(w.cfg.AppURL, "/") + "/projects/" + ids.Encode(ids.Project, project) + "/firewall"
}

// tell publishes a project's blocked calls and alerts its organization,
// at most once an hour.
func (w *Watcher) tell(ctx context.Context, calls []blockedCall) {
	project, org := calls[0].ProjectID, calls[0].OrgID
	url := w.url(project)
	if w.cfg.Webhooks != nil {
		listed := calls[:min(len(calls), maxPublished)]
		p := BlockedPayload{Project: ids.Encode(ids.Project, project), Calls: make([]BlockedCall, len(listed)),
			More: len(calls) - len(listed), URL: url}
		for i, b := range listed {
			c := BlockedCall{Request: b.Trace, Time: b.TS.UTC(), BlockedBy: b.BlockedBy, Policy: b.Policy, Detectors: b.Detectors,
				Key: ids.Encode(ids.APIKey, b.KeyID), Developer: b.Developer, Route: b.Route, Model: b.Model}
			if c.Detectors == nil {
				c.Detectors = []string{}
			}
			p.Calls[i] = c
		}
		if err := w.cfg.Webhooks.Publish(ctx, org, EventBlocked.Name, map[string]any{"firewall": p}); err != nil {
			slog.Warn("firewall: publishing a webhook", "project", project, "err", err)
		}
	}
	var found []string
	for _, b := range calls {
		found = append(found, b.Detectors...)
		if b.Policy != "" {
			found = append(found, "policy "+b.Policy)
		}
	}
	slices.Sort(found)
	found = slices.Compact(found)
	calledN := "a call"
	if len(calls) > 1 {
		calledN = strconv.Itoa(len(calls)) + " calls"
	}
	traces := make([]string, 0, min(len(calls), 20))
	for _, b := range calls[:min(len(calls), 20)] {
		traces = append(traces, b.Trace)
	}
	w.cfg.Activity.Record(ctx, activity.Entry{Org: org, Project: project, Kind: EventBlocked.Name, Severity: activity.Warning,
		Title: "The firewall refused " + calledN, Body: "They held " + strings.Join(found, ", ") + ".", URL: url,
		Data: map[string]any{"calls": len(calls), "found": found, "requests": traces}})
	if w.cfg.Notifier == nil || !w.once(ctx, w.cfg.KV.Key("firewall", "alerted", project.String()), alertEvery) {
		return
	}
	err := w.cfg.Notifier.Alert(ctx, org, notify.Alert{
		Title:    fmt.Sprintf("The firewall refused %s of %s", calledN, w.name(ctx, project)),
		Body:     "They held " + strings.Join(found, ", ") + ". Further refusals this hour are in the firewall's events.",
		URL:      url,
		Severity: "warning",
	})
	if err != nil {
		slog.Warn("firewall: alerting", "project", project, "err", err)
	}
}

// once reports whether key wasn't set in the last ttl, setting it.
func (w *Watcher) once(ctx context.Context, key string, ttl time.Duration) bool {
	c := w.cfg.KV.Client
	set, err := c.Do(ctx, c.B().Set().Key(key).Value("1").Nx().Ex(ttl).Build()).AsBool()
	return err == nil && set
}

func (w *Watcher) name(ctx context.Context, project uuid.UUID) string {
	if w.cfg.ProjectName != nil {
		if n, err := w.cfg.ProjectName(ctx, project); err == nil && n != "" {
			return "project " + n
		}
	}
	return "project " + ids.Encode(ids.Project, project)
}

type weekRow struct {
	OrgID     uuid.UUID `ch:"org_id"`
	ProjectID uuid.UUID `ch:"project_id"`
	Calls     uint64    `ch:"calls"`
	Blocked   uint64    `ch:"blocked"`
	Tokenized uint64    `ch:"tokenized"`
	Redacted  uint64    `ch:"redacted"`
	Alerted   uint64    `ch:"alerted"`
	Top       []string  `ch:"top"`
}

// Digest sends each organization whose firewalls did something in the
// last 7 days, once a week, what they did: per project, the calls refused,
// tokenized, redacted and alerted on, and what was found most.
func (w *Watcher) Digest(ctx context.Context) error {
	if w.cfg.Events == nil || w.cfg.Notifier == nil {
		return nil
	}
	now := w.cfg.Now()
	var rows []weekRow
	err := w.cfg.Events.Unscoped().Select(ctx, &rows, `SELECT org_id, project_id, uniqExact(trace) AS calls,
		uniqExactIf(trace, blocked) AS blocked, uniqExactIf(trace, action = 'tokenize') AS tokenized,
		uniqExactIf(trace, action = 'redact') AS redacted, uniqExactIf(trace, action = 'alert') AS alerted,
		topKIf(3)(detector, detector != '') AS top
		FROM `+EventsTable+` WHERE ts >= fromUnixTimestamp64Milli(?) AND ts < fromUnixTimestamp64Milli(?)
		GROUP BY org_id, project_id ORDER BY org_id, calls DESC`, now.Add(-digestEvery).UnixMilli(), now.UnixMilli())
	if err != nil {
		return fmt.Errorf("firewall: reading the week: %w", err)
	}
	for i := 0; i < len(rows); {
		j := i
		for j < len(rows) && rows[j].OrgID == rows[i].OrgID {
			j++
		}
		w.digest(ctx, rows[i:j], now)
		i = j
	}
	return nil
}

// digest sends an organization's week, unless it got one this week.
func (w *Watcher) digest(ctx context.Context, rows []weekRow, now time.Time) {
	org := rows[0].OrgID
	if !w.once(ctx, w.cfg.KV.Key("firewall", "digest", org.String()), digestEvery-time.Hour) {
		return
	}
	var b strings.Builder
	var blocked uint64
	for _, r := range rows {
		blocked += r.Blocked
		fmt.Fprintf(&b, "%s: %d refused, %d tokenized, %d redacted, %d alerted", w.name(ctx, r.ProjectID), r.Blocked, r.Tokenized,
			r.Redacted, r.Alerted)
		if len(r.Top) > 0 {
			b.WriteString("; found most: " + strings.Join(r.Top, ", "))
		}
		b.WriteString(".\n")
	}
	severity := "info"
	if blocked > 0 {
		severity = "warning"
	}
	err := w.cfg.Notifier.Alert(ctx, org, notify.Alert{
		Title:    "The firewall's week to " + now.UTC().Format("2 January"),
		Body:     strings.TrimSpace(b.String()),
		URL:      w.url(rows[0].ProjectID),
		Severity: severity,
	})
	if err != nil {
		slog.Warn("firewall: sending the digest", "org", org, "err", err)
	}
}

// WatchArgs runs the watcher (every minute).
type WatchArgs struct{}

// Kind implements river.JobArgs.
func (WatchArgs) Kind() string { return "promptdam_firewall_watch" }

// DigestArgs sends the weekly digests that are due (hourly).
type DigestArgs struct{}

// Kind implements river.JobArgs.
func (DigestArgs) Kind() string { return "promptdam_firewall_digest" }

type watchWorker struct {
	river.WorkerDefaults[WatchArgs]
	w *Watcher
}

func (w *watchWorker) Work(ctx context.Context, _ *river.Job[WatchArgs]) error { return w.w.Check(ctx) }

type digestWorker struct {
	river.WorkerDefaults[DigestArgs]
	w *Watcher
}

func (w *digestWorker) Work(ctx context.Context, _ *river.Job[DigestArgs]) error {
	return w.w.Digest(ctx)
}

// RegisterWorkers runs the watcher every minute, and looks for digests
// due every hour: an organization's comes a week after its last, however
// often the workers restart.
func RegisterWorkers(j *jobs.Jobs, w *Watcher) {
	jobs.Register(j, &watchWorker{w: w})
	jobs.Register(j, &digestWorker{w: w})
	j.Every("promptdam-firewall-watch", time.Minute, func() river.JobArgs { return WatchArgs{} })
	j.Every("promptdam-firewall-digest", time.Hour, func() river.JobArgs { return DigestArgs{} })
}
