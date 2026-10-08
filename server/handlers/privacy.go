package handlers

import (
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/audit"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/privacy"
)

// CodePayloadFree is something a payload-free organization can't turn on.
const CodePayloadFree = "payload_free"

// PrivacyOut is an organization's privacy.
type PrivacyOut struct {
	PayloadFree bool       `json:"payload_free" doc:"No prompt or answer is stored in any project: payload capture, firewall excerpts and the response cache are off and can't be turned on"`
	UpdatedAt   *time.Time `json:"updated_at"`
	UpdatedBy   string     `json:"updated_by" doc:"Who last changed it (usr_…, key_…)"`
}

func privacyOut(s privacy.Settings) PrivacyOut {
	out := PrivacyOut{PayloadFree: s.PayloadFree, UpdatedBy: s.UpdatedBy}
	if !s.UpdatedAt.IsZero() {
		at := s.UpdatedAt.UTC()
		out.UpdatedAt = &at
	}
	return out
}

// PutPrivacyIn sets an organization's privacy.
type PutPrivacyIn struct {
	platform.OrgPath
	PayloadFree bool `json:"payload_free"`
}

// GetPrivacy returns an organization's privacy.
func (h *Handlers) GetPrivacy(ctx *muzak.Context, _ platform.OrgPath) (PrivacyOut, error) {
	a := muzak.From[tenancy.Access](ctx)
	s, err := h.Privacy.Get(ctx.Context(), a.OrgID())
	if err != nil {
		return PrivacyOut{}, err
	}
	return privacyOut(s), nil
}

// PutPrivacy turns payload-free mode on or off.
func (h *Handlers) PutPrivacy(ctx *muzak.Context, in PutPrivacyIn) (PrivacyOut, error) {
	a := muzak.From[tenancy.Access](ctx)
	if in.PayloadFree {
		if err := h.requires(ctx, catalog.PayloadFree); err != nil {
			return PrivacyOut{}, err
		}
	}
	actor := a.Actor()
	s, err := h.Privacy.Put(ctx.Context(), a.OrgID(), in.PayloadFree, actor.ID)
	if err != nil {
		return PrivacyOut{}, err
	}
	if h.Audit != nil {
		_ = audit.Record(audit.Request(ctx, actor), h.Audit, audit.Event{OrgID: a.OrgID(), Action: "privacy.updated",
			TargetType: "organization", Meta: map[string]any{"payload_free": in.PayloadFree}})
	}
	return privacyOut(s), nil
}

// payloadFree refuses turning on, in a project of a payload-free
// organization, what would keep content.
func (h *Handlers) payloadFree(ctx *muzak.Context, what string) error {
	if h.Privacy == nil {
		return nil
	}
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	if !h.Privacy.PayloadFree(ctx.Context(), pa.OrgID()) {
		return nil
	}
	return muzak.Conflict("the organization is payload-free: " + what + " would keep prompts or answers").
		WithCode(CodePayloadFree)
}
