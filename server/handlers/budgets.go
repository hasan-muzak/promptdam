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
	"github.com/hasan-muzak/promptdam/server/features/budgets"
)

// Error codes of the budget routes.
const (
	CodeInvalidBudget = "invalid_budget"
	CodeBudgetLimit   = "budget_limit"
)

// BudgetOut is a budget with its current window.
type BudgetOut struct {
	ID          string    `json:"id" doc:"Budget ID (bdg_…)"`
	Name        string    `json:"name"`
	Scope       string    `json:"scope" doc:"project, key, developer or route"`
	Target      string    `json:"target" doc:"The key (key_…), developer or route; empty for the project"`
	Window      string    `json:"window" doc:"day or month (UTC)"`
	LimitMicros int64     `json:"limit_micros" doc:"Millionths of a dollar per window"`
	Action      string    `json:"action" doc:"alert, or block: the gateway refuses the scope's calls once it is spent"`
	Thresholds  []int     `json:"thresholds" doc:"Percents of the limit that alert"`
	Channels    []string  `json:"channels" doc:"Channel IDs (chn_…); empty for all of the organization's"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	// The current window.
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	SpentMicros int64     `json:"spent_micros"`
	Percent     float64   `json:"percent" doc:"Share of the limit spent (100: all of it)"`
}

// BudgetIn is a budget's fields.
type BudgetIn struct {
	Name        string   `json:"name"`
	Scope       string   `json:"scope" doc:"project, key, developer or route"`
	Target      string   `json:"target,omitempty" doc:"An API key (key_…), a developer or a route's name; omit for the project"`
	Window      string   `json:"window" doc:"day or month (UTC)"`
	LimitMicros int64    `json:"limit_micros" doc:"Millionths of a dollar per window, e.g. 50000000 for $50"`
	Action      string   `json:"action,omitempty" doc:"alert (default) or block"`
	Thresholds  []int    `json:"thresholds,omitempty" doc:"Percents of the limit that alert, 1 to 1000 (default 80 and 100)"`
	Channels    []string `json:"channels" doc:"Channel IDs (chn_…); empty for all"`
	Enabled     *bool    `json:"enabled,omitempty" doc:"Default true"`
}

func (in *BudgetIn) validate(v *muzak.Validation) {
	v.String(&in.Name).Trim().Required().MaxLen(100)
	v.String(&in.Scope).OneOf("project", "key", "developer", "route")
	v.String(&in.Window).OneOf("day", "month")
	v.Number(&in.LimitMicros).Min(1).Max(1_000_000_000_000)
	v.Slice(&in.Thresholds).MaxItems(5)
	v.Slice(&in.Channels).MaxItems(20)
}

// CreateBudgetIn adds a budget.
type CreateBudgetIn struct {
	platform.ProjectPath
	BudgetIn
}

// Validate implements muzak.Validatable.
func (in *CreateBudgetIn) Validate(v *muzak.Validation) { in.validate(v) }

// BudgetPath names a budget.
type BudgetPath struct {
	platform.ProjectPath
	Budget string `path:"budget" doc:"Budget ID (bdg_…)"`
}

// UpdateBudgetIn replaces a budget.
type UpdateBudgetIn struct {
	BudgetPath
	BudgetIn
}

// Validate implements muzak.Validatable.
func (in *UpdateBudgetIn) Validate(v *muzak.Validation) { in.validate(v) }

// input converts the input: key targets from their TypeID, channels checked
// to be the organization's.
func (h *Handlers) budgetInput(ctx *muzak.Context, org uuid.UUID, in BudgetIn) (budgets.Input, error) {
	target := in.Target
	if in.Scope == budgets.ScopeKey {
		id, err := ids.Decode(ids.APIKey, in.Target)
		if err != nil {
			return budgets.Input{}, muzak.UnprocessableEntity("a key budget names an API key (key_…)").WithCode(CodeInvalidBudget)
		}
		target = id.String()
	}
	action := in.Action
	if action == "" {
		action = "alert"
	}
	channels, err := h.ownChannels(ctx, org, in.Channels)
	if err != nil {
		return budgets.Input{}, err
	}
	return budgets.Input{
		Name: in.Name, Scope: in.Scope, Target: target, Window: in.Window, LimitMicros: in.LimitMicros, Action: action,
		Thresholds: in.Thresholds, Channels: channels, Enabled: in.Enabled == nil || *in.Enabled,
	}, nil
}

// developerBudget refuses a new per-developer budget on a plan without
// per-developer keys; one already set for that developer stays.
func (h *Handlers) developerBudget(ctx *muzak.Context, in budgets.Input, old *pdent.Budget) error {
	if in.Scope != budgets.ScopeDeveloper || in.Target == "" {
		return nil // a developer budget without its developer is refused as invalid
	}
	if old != nil && string(old.Scope) == budgets.ScopeDeveloper && old.Target == in.Target {
		return nil
	}
	return h.requires(ctx, catalog.DeveloperKeys)
}

func (h *Handlers) ownChannels(ctx *muzak.Context, org uuid.UUID, raw []string) ([]uuid.UUID, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	channels, err := h.Notifier.Channels(ctx.Context(), org)
	if err != nil {
		return nil, err
	}
	own := make(map[uuid.UUID]bool, len(channels))
	for _, c := range channels {
		own[c.ID] = true
	}
	out := make([]uuid.UUID, 0, len(raw))
	for _, r := range raw {
		id, err := ids.Decode(ids.Channel, r)
		if err != nil || !own[id] {
			return nil, httpx.NotFound("channel")
		}
		out = append(out, id)
	}
	return out, nil
}

func (h *Handlers) budgetOut(ctx *muzak.Context, b *pdent.Budget) (BudgetOut, error) {
	st, err := h.Budgets.Status(ctx.Context(), b)
	if err != nil {
		return BudgetOut{}, err
	}
	target := b.Target
	if b.Scope == "key" {
		if id, err := uuid.Parse(b.Target); err == nil {
			target = ids.Encode(ids.APIKey, id)
		}
	}
	channels := make([]string, 0, len(b.ChannelIds))
	for _, c := range b.ChannelIds {
		if id, err := uuid.Parse(c); err == nil {
			channels = append(channels, ids.Encode(ids.Channel, id))
		}
	}
	return BudgetOut{
		ID: ids.Encode(budgets.IDPrefix, b.ID), Name: b.Name, Scope: string(b.Scope), Target: target, Window: string(b.Window),
		LimitMicros: b.LimitMicros, Action: string(b.Action), Thresholds: b.Thresholds, Channels: channels, Enabled: b.Enabled,
		CreatedAt: b.CreatedAt, WindowStart: st.WindowStart, WindowEnd: st.WindowEnd, SpentMicros: st.SpentMicros, Percent: st.Percent,
	}, nil
}

// ListBudgets lists a project's budgets with their current window.
func (h *Handlers) ListBudgets(ctx *muzak.Context, _ platform.ProjectPath) (httpx.List[BudgetOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	bs, err := h.Budgets.List(ctx.Context(), pa.Project.ID)
	if err != nil {
		return httpx.List[BudgetOut]{}, err
	}
	out := make([]BudgetOut, 0, len(bs))
	for _, b := range bs {
		o, err := h.budgetOut(ctx, b)
		if err != nil {
			return httpx.List[BudgetOut]{}, err
		}
		out = append(out, o)
	}
	return httpx.List[BudgetOut]{Data: out}, nil
}

// CreateBudget adds a budget.
func (h *Handlers) CreateBudget(ctx *muzak.Context, in CreateBudgetIn) (BudgetOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	input, err := h.budgetInput(ctx, pa.OrgID(), in.BudgetIn)
	if err != nil {
		return BudgetOut{}, err
	}
	if err := h.developerBudget(ctx, input, nil); err != nil {
		return BudgetOut{}, err
	}
	b, err := h.Budgets.Create(ctx.Context(), pa.OrgID(), pa.Project.ID, input)
	if err != nil {
		return BudgetOut{}, budgetFail(err)
	}
	return h.budgetOut(ctx, b)
}

// GetBudget returns a budget with its current window.
func (h *Handlers) GetBudget(ctx *muzak.Context, in BudgetPath) (BudgetOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := budgetID(in.Budget)
	if err != nil {
		return BudgetOut{}, err
	}
	b, err := h.Budgets.Get(ctx.Context(), pa.Project.ID, id)
	if err != nil {
		return BudgetOut{}, budgetFail(err)
	}
	return h.budgetOut(ctx, b)
}

// UpdateBudget replaces a budget.
func (h *Handlers) UpdateBudget(ctx *muzak.Context, in UpdateBudgetIn) (BudgetOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := budgetID(in.Budget)
	if err != nil {
		return BudgetOut{}, err
	}
	input, err := h.budgetInput(ctx, pa.OrgID(), in.BudgetIn)
	if err != nil {
		return BudgetOut{}, err
	}
	if input.Scope == budgets.ScopeDeveloper {
		old, err := h.Budgets.Get(ctx.Context(), pa.Project.ID, id)
		if err != nil {
			return BudgetOut{}, budgetFail(err)
		}
		if err := h.developerBudget(ctx, input, old); err != nil {
			return BudgetOut{}, err
		}
	}
	b, err := h.Budgets.Update(ctx.Context(), pa.Project.ID, id, input)
	if err != nil {
		return BudgetOut{}, budgetFail(err)
	}
	return h.budgetOut(ctx, b)
}

// DeleteBudget removes a budget.
func (h *Handlers) DeleteBudget(ctx *muzak.Context, in BudgetPath) (muzak.Empty, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := budgetID(in.Budget)
	if err != nil {
		return muzak.Empty{}, err
	}
	return muzak.Empty{}, budgetFail(h.Budgets.Delete(ctx.Context(), pa.Project.ID, id))
}

func budgetID(raw string) (uuid.UUID, error) {
	id, err := ids.Decode(budgets.IDPrefix, raw)
	if err != nil {
		return id, httpx.NotFound("budget")
	}
	return id, nil
}

func budgetFail(err error) error {
	var bad *budgets.InvalidError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return muzak.UnprocessableEntity(bad.Reason).WithCode(CodeInvalidBudget)
	case errors.Is(err, budgets.ErrNotFound):
		return httpx.NotFound("budget")
	case errors.Is(err, budgets.ErrLimit):
		return muzak.Conflict("a project has at most 100 budgets").WithCode(CodeBudgetLimit)
	}
	return err
}
