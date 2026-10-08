package handlers

import (
	"encoding/json"
	"errors"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/payloads"
)

// Error codes of the payload routes.
const (
	CodeInvalidCapture = "invalid_capture"
	CodePayloadGone    = "payload_gone"
)

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

// PayloadOut is what was kept of a call: secrets, personal data and the
// project's own patterns masked.
type PayloadOut struct {
	Request   string           `json:"request" doc:"The call's trace (req_…)"`
	APIFormat string           `json:"api_format"`
	Stream    bool             `json:"stream"`
	Status    int              `json:"status"`
	Body      json.RawMessage  `json:"body" doc:"The request as the provider got it, masked; null when it was too large to keep (over 1 MiB)"`
	Answer    *payloads.Answer `json:"answer" doc:"What the model answered (text, tool calls, refusal), masked; null when answers aren't kept, the format's can't be read, or the call failed"`
	Error     json.RawMessage  `json:"error,omitempty" doc:"A failed call's error body, masked"`
	Truncated []string         `json:"truncated" doc:"What was cut or left out: request (too large), answer (text cut at 256 KiB)"`
}

// GetPayload returns what was kept of a call.
func (h *Handlers) GetPayload(ctx *muzak.Context, in RequestPath) (PayloadOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	r, err := h.Analytics.Request(ctx.Context(), pa.Project.ID, in.Request)
	if err != nil {
		return PayloadOut{}, analyticsFail(err)
	}
	if r.PayloadRef == "" {
		return PayloadOut{}, httpx.NotFound("payload")
	}
	p, err := h.PayloadStore.Read(ctx.Context(), pa.OrgID(), pa.Project.ID, r.PayloadRef)
	if errors.Is(err, payloads.ErrGone) || errors.Is(err, payloads.ErrRef) {
		return PayloadOut{}, muzak.NotFound("the payload expired or was deleted").WithCode(CodePayloadGone)
	}
	if err != nil {
		return PayloadOut{}, err
	}
	h.audited(ctx, "payload.viewed", "request", in.Request, nil)
	return PayloadOut{Request: in.Request, APIFormat: p.Format, Stream: p.Stream, Status: p.Status, Body: p.Request,
		Answer: p.Answer, Error: p.Error, Truncated: p.Truncated}, nil
}

// DeletedPayloadsOut says how many segments of payloads were deleted.
type DeletedPayloadsOut struct {
	Segments int `json:"segments" doc:"Objects deleted, each holding many calls' payloads"`
}

// DeletePayloads deletes every payload the project kept, now.
func (h *Handlers) DeletePayloads(ctx *muzak.Context, _ platform.ProjectPath) (DeletedPayloadsOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	n, err := h.PayloadStore.DeleteProject(ctx.Context(), pa.OrgID(), pa.Project.ID)
	if err != nil {
		return DeletedPayloadsOut{}, err
	}
	h.audited(ctx, "payloads.deleted", "project", ids.Encode(ids.Project, pa.Project.ID), map[string]any{"segments": n})
	return DeletedPayloadsOut{Segments: n}, nil
}
