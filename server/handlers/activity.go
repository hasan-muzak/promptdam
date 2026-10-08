package handlers

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/activity"
)

// ListActivityIn filters a project's activity.
type ListActivityIn struct {
	platform.ProjectPath
	httpx.Page
	Kind    string `query:"kind" doc:"Only this kind: budget.threshold_reached, budget.exceeded, router.ready, router.tier_demoted, firewall.blocked, alert.fired, alert.resolved"`
	Subject string `query:"subject" doc:"Only what happened to this budget (bdg_…), router (rtr_…) or alert rule (alr_…): its history"`
}

// ActivityOut is something that happened to the project.
type ActivityOut struct {
	ID       string         `json:"id" doc:"Activity ID (act_…)"`
	Time     time.Time      `json:"time"`
	Kind     string         `json:"kind" doc:"As its webhook event names it"`
	Severity string         `json:"severity" doc:"info, warning or critical"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Subject  string         `json:"subject" doc:"What it is about: a budget, a router, an alert rule; empty for the project"`
	URL      string         `json:"url" doc:"The dashboard's page for it"`
	Data     map[string]any `json:"data" doc:"Its numbers, as its webhook carries them"`
}

// ListActivity lists what happened to a project, newest first.
func (h *Handlers) ListActivity(ctx *muzak.Context, in ListActivityIn) (httpx.List[ActivityOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	var after *activity.Cursor
	if in.Cursor != "" {
		c, err := decodeActivityCursor(in.Cursor)
		if err != nil {
			return httpx.List[ActivityOut]{}, muzak.UnprocessableEntity("the cursor is invalid").WithCode(CodeInvalidQuery)
		}
		after = &c
	}
	rows, next, err := h.Activity.List(ctx.Context(), pa.Project.ID, activity.Filter{Kind: in.Kind, Subject: in.Subject}, after,
		in.Limit)
	if err != nil {
		return httpx.List[ActivityOut]{}, err
	}
	out := httpx.List[ActivityOut]{Data: make([]ActivityOut, len(rows))}
	for i, r := range rows {
		out.Data[i] = activityOut(r)
	}
	if next != nil {
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(next.TS.UnixMilli(), 10) + ":" +
			ids.Encode(activity.IDPrefix, next.ID)))
	}
	return out, nil
}

func activityOut(r activity.Row) ActivityOut {
	return ActivityOut{ID: ids.Encode(activity.IDPrefix, r.ID), Time: r.TS.UTC(), Kind: r.Kind, Severity: r.Severity,
		Title: r.Title, Body: r.Body, Subject: r.Subject, URL: r.URL, Data: r.Data()}
}

func decodeActivityCursor(s string) (activity.Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	ms, id, ok := strings.Cut(string(raw), ":")
	n, perr := strconv.ParseInt(ms, 10, 64)
	u, ierr := ids.Decode(activity.IDPrefix, id)
	if err != nil || !ok || perr != nil || ierr != nil {
		return activity.Cursor{}, errors.New("handlers: the cursor is invalid")
	}
	return activity.Cursor{TS: time.UnixMilli(n), ID: u}, nil
}
