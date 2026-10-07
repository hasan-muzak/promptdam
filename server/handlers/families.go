package handlers

import (
	"errors"
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/families"
)

// CodeInvalidFamily reports a family request that can't be answered.
const CodeInvalidFamily = "invalid_family"

// FamilyOut is a prompt family's sums over a range.
type FamilyOut struct {
	ID              string `json:"id" doc:"16 hex characters, or name:<name> for families named with X-Promptdam-Family"`
	Label           string `json:"label" doc:"The name the project gave it; empty until named"`
	Requests        uint64 `json:"requests"`
	Errors          uint64 `json:"errors"`
	InputTokens     uint64 `json:"input_tokens"`
	OutputTokens    uint64 `json:"output_tokens"`
	CacheReadTokens uint64 `json:"cache_read_tokens" doc:"Input tokens the providers' prompt caches served"`
	CostMicros      int64  `json:"cost_micros"`
}

func familyOut(f families.Family) FamilyOut {
	return FamilyOut{ID: f.ID, Label: f.Label, Requests: f.Requests, Errors: f.Errors, InputTokens: f.InputTokens,
		OutputTokens: f.OutputTokens, CacheReadTokens: f.CacheReadTokens, CostMicros: f.CostMicros}
}

// ListFamiliesIn asks for a project's top families.
type ListFamiliesIn struct {
	platform.ProjectPath
	TimeRange
	Limit int `query:"limit" default:"50" doc:"1 to 100"`
}

// Validate implements muzak.Validatable.
func (in *ListFamiliesIn) Validate(v *muzak.Validation) { v.Number(&in.Limit).Min(1).Max(100) }

// ListFamilies lists a project's prompt families by spend.
func (h *Handlers) ListFamilies(ctx *muzak.Context, in ListFamiliesIn) (httpx.List[FamilyOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := in.bounds(7 * 24 * time.Hour)
	if err != nil {
		return httpx.List[FamilyOut]{}, err
	}
	fs, err := h.Families.Top(ctx.Context(), pa.Project.ID, from, to, in.Limit)
	if err != nil {
		return httpx.List[FamilyOut]{}, familyFail(err)
	}
	out := httpx.List[FamilyOut]{Data: make([]FamilyOut, len(fs))}
	for i, f := range fs {
		out.Data[i] = familyOut(f)
	}
	return out, nil
}

// FamilyPath names a family.
type FamilyPath struct {
	platform.ProjectPath
	Family string `path:"family" doc:"The family's ID"`
}

// GetFamilyIn asks for one family over a range.
type GetFamilyIn struct {
	FamilyPath
	TimeRange
}

// FamilyModelOut is a family's calls asked of one model and served by one.
type FamilyModelOut struct {
	RequestedModel string         `json:"requested_model"`
	ServedModel    string         `json:"served_model"`
	Requests       uint64         `json:"requests"`
	Errors         uint64         `json:"errors"`
	InputTokens    uint64         `json:"input_tokens"`
	OutputTokens   uint64         `json:"output_tokens"`
	CostMicros     int64          `json:"cost_micros"`
	Latency        PercentilesOut `json:"latency_ms"`
}

// FamilyDetailOut is one family over a range.
type FamilyDetailOut struct {
	FamilyOut
	Models []FamilyModelOut `json:"models"`
	Series []PointOut       `json:"series" doc:"Per day"`
}

// GetFamily returns a family's sums, model mix and daily series.
func (h *Handlers) GetFamily(ctx *muzak.Context, in GetFamilyIn) (FamilyDetailOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := in.bounds(7 * 24 * time.Hour)
	if err != nil {
		return FamilyDetailOut{}, err
	}
	d, err := h.Families.Get(ctx.Context(), pa.Project.ID, in.Family, from, to)
	if err != nil {
		return FamilyDetailOut{}, familyFail(err)
	}
	out := FamilyDetailOut{FamilyOut: familyOut(d.Family), Models: make([]FamilyModelOut, len(d.Models)),
		Series: make([]PointOut, len(d.Series))}
	for i, m := range d.Models {
		out.Models[i] = FamilyModelOut{RequestedModel: m.RequestedModel, ServedModel: m.ServedModel, Requests: m.Requests,
			Errors: m.Errors, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens, CostMicros: m.CostMicros,
			Latency: percentiles(m.Latency)}
	}
	for i, p := range d.Series {
		out.Series[i] = PointOut{Time: p.T.UTC(), Requests: p.Requests, CostMicros: p.CostMicros}
	}
	return out, nil
}

// LabelFamilyIn names a family.
type LabelFamilyIn struct {
	FamilyPath
	Label string `json:"label" doc:"Up to 100 characters; empty removes the name"`
}

// LabelFamilyOut is a family's name.
type LabelFamilyOut struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// LabelFamily names a family.
func (h *Handlers) LabelFamily(ctx *muzak.Context, in LabelFamilyIn) (LabelFamilyOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	if err := h.Families.SetLabel(ctx.Context(), pa.OrgID(), pa.Project.ID, in.Family, in.Label); err != nil {
		return LabelFamilyOut{}, familyFail(err)
	}
	return LabelFamilyOut{ID: in.Family, Label: in.Label}, nil
}

func familyFail(err error) error {
	switch {
	case errors.Is(err, families.ErrUnknown):
		return httpx.NotFound("family")
	case errors.Is(err, families.ErrRange):
		return muzak.UnprocessableEntity("the time range must start before it ends and cover at most 400 days").
			WithCode(CodeInvalidQuery)
	case errors.Is(err, families.ErrLabel):
		return muzak.UnprocessableEntity("a label is at most 100 characters").WithCode(CodeInvalidFamily)
	}
	return err
}
