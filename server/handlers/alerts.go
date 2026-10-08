package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/catalog"
	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/features/alerts"
)

// Error codes of the alert rule routes.
const (
	CodeInvalidAlert   = "invalid_alert"
	CodeAlertNameTaken = "alert_name_taken"
	CodeAlertLimit     = "alert_limit"
)

// AlertRuleIn is an alert rule as it is saved.
type AlertRuleIn struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind" doc:"spend_spike, error_rate, provider_down or latency"`
	Threshold       float64  `json:"threshold,omitempty" doc:"spend_spike: times the usual hour (default 3); error_rate: the share of calls failing at the provider (default 0.1); provider_down: a provider's share of failing calls (default 0.5); latency: p95 in ms (default 10000)"`
	WindowMinutes   int      `json:"window_minutes,omitempty" doc:"5, 15, 30 or 60 (default 15; provider_down 5; spend_spike always 60)"`
	MinCalls        int      `json:"min_calls,omitempty" doc:"Fewer calls in the window can't fire it (default 20; provider_down 5)"`
	MinMicros       int64    `json:"min_micros,omitempty" doc:"spend_spike: the least the hour must cost, in millionths of a dollar (default $5)"`
	Provider        string   `json:"provider,omitempty" doc:"Only this provider's calls (its name in the project); empty for all, and provider_down watches each"`
	Model           string   `json:"model,omitempty" doc:"Only calls to this model"`
	Channels        []string `json:"channels" doc:"Channel IDs (chn_…); empty for all of the organization's"`
	Enabled         *bool    `json:"enabled,omitempty" doc:"Default true"`
	CooldownMinutes int      `json:"cooldown_minutes,omitempty" doc:"While it holds, it alerts again after this long (default 60)"`
}

// AlertRuleOut is an alert rule and where it stands.
type AlertRuleOut struct {
	ID              string     `json:"id" doc:"Alert rule ID (alr_…)"`
	Name            string     `json:"name"`
	Kind            string     `json:"kind"`
	Threshold       float64    `json:"threshold"`
	WindowMinutes   int        `json:"window_minutes"`
	MinCalls        int        `json:"min_calls"`
	MinMicros       int64      `json:"min_micros"`
	Provider        string     `json:"provider"`
	Model           string     `json:"model"`
	Channels        []string   `json:"channels"`
	Enabled         bool       `json:"enabled"`
	CooldownMinutes int        `json:"cooldown_minutes"`
	Firing          bool       `json:"firing" doc:"Its condition held at the last evaluation"`
	FiringOn        []string   `json:"firing_on" doc:"provider_down: the providers that look down"`
	LastValue       float64    `json:"last_value" doc:"What the last evaluation measured, in the threshold's unit"`
	EvaluatedAt     *time.Time `json:"evaluated_at" doc:"Rules are evaluated every minute while the plan includes them"`
	FiredAt         *time.Time `json:"fired_at"`
	ResolvedAt      *time.Time `json:"resolved_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func alertOut(r *pdent.AlertRule) AlertRuleOut {
	out := AlertRuleOut{ID: ids.Encode(alerts.IDPrefix, r.ID), Name: r.Name, Kind: string(r.Kind), Threshold: r.Threshold,
		WindowMinutes: r.WindowMinutes, MinCalls: r.MinCalls, MinMicros: r.MinMicros, Provider: r.Provider, Model: r.Model,
		Channels: make([]string, 0, len(r.ChannelIds)), Enabled: r.Enabled, CooldownMinutes: r.CooldownMinutes,
		Firing: r.Firing, FiringOn: r.FiringOn, LastValue: r.LastValue, EvaluatedAt: r.EvaluatedAt, FiredAt: r.FiredAt,
		ResolvedAt: r.ResolvedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	for _, c := range r.ChannelIds {
		if id, err := uuid.Parse(c); err == nil {
			out.Channels = append(out.Channels, ids.Encode(ids.Channel, id))
		}
	}
	if out.FiringOn == nil {
		out.FiringOn = []string{}
	}
	return out
}

// CreateAlertIn creates an alert rule.
type CreateAlertIn struct {
	platform.ProjectPath
	AlertRuleIn
}

// AlertPath names an alert rule.
type AlertPath struct {
	platform.ProjectPath
	Alert string `path:"alert" doc:"Alert rule ID (alr_…)"`
}

// UpdateAlertIn replaces an alert rule.
type UpdateAlertIn struct {
	AlertPath
	AlertRuleIn
}

func (h *Handlers) alertInput(ctx *muzak.Context, org uuid.UUID, in AlertRuleIn) (alerts.Input, error) {
	channels, err := h.ownChannels(ctx, org, in.Channels)
	if err != nil {
		return alerts.Input{}, err
	}
	return alerts.Input{Name: in.Name, Kind: in.Kind, Threshold: in.Threshold, WindowMinutes: in.WindowMinutes,
		MinCalls: in.MinCalls, MinMicros: in.MinMicros, Provider: in.Provider, Model: in.Model, Channels: channels,
		Enabled: in.Enabled == nil || *in.Enabled, CooldownMinutes: in.CooldownMinutes}, nil
}

// ListAlerts lists a project's alert rules.
func (h *Handlers) ListAlerts(ctx *muzak.Context, _ platform.ProjectPath) (httpx.List[AlertRuleOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	rs, err := h.Alerts.List(ctx.Context(), pa.Project.ID)
	if err != nil {
		return httpx.List[AlertRuleOut]{}, err
	}
	out := httpx.List[AlertRuleOut]{Data: make([]AlertRuleOut, len(rs))}
	for i, r := range rs {
		out.Data[i] = alertOut(r)
	}
	return out, nil
}

// CreateAlert adds an alert rule.
func (h *Handlers) CreateAlert(ctx *muzak.Context, in CreateAlertIn) (AlertRuleOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	if err := h.requires(ctx, catalog.Alerts); err != nil {
		return AlertRuleOut{}, err
	}
	input, err := h.alertInput(ctx, pa.OrgID(), in.AlertRuleIn)
	if err != nil {
		return AlertRuleOut{}, err
	}
	r, err := h.Alerts.Create(ctx.Context(), pa.OrgID(), pa.Project.ID, input)
	if err != nil {
		return AlertRuleOut{}, alertFail(err)
	}
	h.audited(ctx, "alert_rule.created", "alert_rule", ids.Encode(alerts.IDPrefix, r.ID), map[string]any{"name": r.Name, "kind": r.Kind, "threshold": r.Threshold, "window_minutes": r.WindowMinutes, "enabled": r.Enabled})
	return alertOut(r), nil
}

func alertID(raw string) (uuid.UUID, error) {
	id, err := ids.Decode(alerts.IDPrefix, raw)
	if err != nil {
		return id, httpx.NotFound("alert rule")
	}
	return id, nil
}

// GetAlert returns an alert rule.
func (h *Handlers) GetAlert(ctx *muzak.Context, in AlertPath) (AlertRuleOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := alertID(in.Alert)
	if err != nil {
		return AlertRuleOut{}, err
	}
	r, err := h.Alerts.Get(ctx.Context(), pa.Project.ID, id)
	if err != nil {
		return AlertRuleOut{}, alertFail(err)
	}
	return alertOut(r), nil
}

// UpdateAlert replaces an alert rule.
func (h *Handlers) UpdateAlert(ctx *muzak.Context, in UpdateAlertIn) (AlertRuleOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	if err := h.requires(ctx, catalog.Alerts); err != nil {
		return AlertRuleOut{}, err
	}
	id, err := alertID(in.Alert)
	if err != nil {
		return AlertRuleOut{}, err
	}
	input, err := h.alertInput(ctx, pa.OrgID(), in.AlertRuleIn)
	if err != nil {
		return AlertRuleOut{}, err
	}
	r, err := h.Alerts.Update(ctx.Context(), pa.Project.ID, id, input)
	if err != nil {
		return AlertRuleOut{}, alertFail(err)
	}
	h.audited(ctx, "alert_rule.updated", "alert_rule", ids.Encode(alerts.IDPrefix, r.ID), map[string]any{"name": r.Name, "kind": r.Kind, "threshold": r.Threshold, "window_minutes": r.WindowMinutes, "enabled": r.Enabled})
	return alertOut(r), nil
}

// DeleteAlert removes an alert rule.
func (h *Handlers) DeleteAlert(ctx *muzak.Context, in AlertPath) (muzak.Empty, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := alertID(in.Alert)
	if err != nil {
		return muzak.Empty{}, err
	}
	if err := h.Alerts.Delete(ctx.Context(), pa.Project.ID, id); err != nil {
		return muzak.Empty{}, alertFail(err)
	}
	h.audited(ctx, "alert_rule.deleted", "alert_rule", in.Alert, map[string]any{})
	return muzak.Empty{}, nil
}

func alertFail(err error) error {
	var bad *alerts.InvalidError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return muzak.UnprocessableEntity(bad.Reason).WithCode(CodeInvalidAlert)
	case errors.Is(err, alerts.ErrNotFound):
		return httpx.NotFound("alert rule")
	case errors.Is(err, alerts.ErrNameTaken):
		return muzak.Conflict("another alert rule of the project has this name").WithCode(CodeAlertNameTaken)
	case errors.Is(err, alerts.ErrLimit):
		return muzak.Conflict("a project has at most 20 alert rules").WithCode(CodeAlertLimit)
	}
	return err
}
