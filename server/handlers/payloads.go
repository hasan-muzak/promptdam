package handlers

import (
	"errors"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/payloads"
)

// CodeInvalidCapture is capture settings that can't be saved.
const CodeInvalidCapture = "invalid_capture"

// CaptureSettings are whether a project keeps its calls' payloads.
type CaptureSettings struct {
	Enabled    bool    `json:"enabled" doc:"Keep the payloads of the project's calls: the request as the provider got it and the answer the client got, secrets and personal data always masked, as long as the plan keeps logs"`
	SampleRate float64 `json:"sample_rate" doc:"The share of calls kept, 0 to 1 (default 1: every call)"`
	Answers    bool    `json:"answers" doc:"Keep answers too, not only requests (default true)"`
}

// PutCaptureIn replaces a project's capture settings.
type PutCaptureIn struct {
	platform.ProjectPath
	CaptureSettings
}

// GetCapture returns whether a project keeps its calls' payloads.
func (h *Handlers) GetCapture(ctx *muzak.Context, _ platform.ProjectPath) (CaptureSettings, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	s, err := h.Payloads.Get(ctx.Context(), pa.Project.ID)
	if err != nil {
		return CaptureSettings{}, err
	}
	return CaptureSettings(s), nil
}

// PutCapture replaces whether a project keeps its calls' payloads.
func (h *Handlers) PutCapture(ctx *muzak.Context, in PutCaptureIn) (CaptureSettings, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	s, err := h.Payloads.Put(ctx.Context(), pa.OrgID(), pa.Project.ID, payloads.Settings(in.CaptureSettings))
	if errors.Is(err, payloads.ErrInvalid) {
		return CaptureSettings{}, muzak.UnprocessableEntity("sample_rate is 0 to 1").WithCode(CodeInvalidCapture)
	}
	if err != nil {
		return CaptureSettings{}, err
	}
	h.audited(ctx, "payloads.updated", "project", ids.Encode(ids.Project, pa.Project.ID), map[string]any{
		"enabled": s.Enabled, "sample_rate": s.SampleRate, "answers": s.Answers})
	return CaptureSettings(s), nil
}
