package handlers

import (
	"errors"

	"github.com/google/uuid"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/features/keysettings"
)

// Error codes of the key settings routes.
const (
	CodeInvalidKeySettings = "invalid_key_settings"
	CodeKeyNotForGateway   = "key_not_for_gateway"
)

// KeySettingsPath names an API key.
type KeySettingsPath struct {
	platform.ProjectPath
	Key string `path:"key" doc:"API key ID (key_…)"`
}

// KeySettings are what the gateway applies to an API key.
type KeySettings struct {
	AllowedModels []string `json:"allowed_models" doc:"Models the key may call (a trailing * matches a prefix, e.g. claude-*); empty: every model"`
	Tags          []string `json:"tags" doc:"Tags its requests carry, for analytics"`
	Developer     string   `json:"developer" doc:"Who uses the key, for attribution and per-developer budgets"`
	RPM           int      `json:"rpm" doc:"Requests per minute; 0: no limit of its own"`
	Route         string   `json:"route" doc:"The route its requests take unless they name one (X-Promptdam-Route)"`
}

// PutKeySettingsIn replaces a key's settings.
type PutKeySettingsIn struct {
	KeySettingsPath
	KeySettings
}

// Validate implements muzak.Validatable.
func (in *PutKeySettingsIn) Validate(v *muzak.Validation) {
	v.Slice(&in.AllowedModels).MaxItems(200)
	v.Slice(&in.Tags).MaxItems(20)
	v.String(&in.Developer).Trim().MaxLen(200)
	v.Number(&in.RPM).Min(0).Max(1_000_000)
	v.String(&in.Route).Trim().MaxLen(100)
}

// GetKeySettings returns a key's gateway settings.
func (h *Handlers) GetKeySettings(ctx *muzak.Context, in KeySettingsPath) (KeySettings, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	key, err := keyID(in.Key)
	if err != nil {
		return KeySettings{}, err
	}
	s, err := h.Keys.Get(ctx.Context(), pa.OrgID(), pa.Project.ID, key)
	if err != nil {
		return KeySettings{}, keyFail(err)
	}
	return keySettingsOut(s), nil
}

// PutKeySettings replaces a key's gateway settings.
func (h *Handlers) PutKeySettings(ctx *muzak.Context, in PutKeySettingsIn) (KeySettings, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	key, err := keyID(in.Key)
	if err != nil {
		return KeySettings{}, err
	}
	s, err := h.Keys.Put(ctx.Context(), pa.OrgID(), pa.Project.ID, key, keysettings.Settings{
		AllowedModels: in.AllowedModels, Tags: in.Tags, Developer: in.Developer, RPM: in.RPM, Route: in.Route,
	})
	if err != nil {
		return KeySettings{}, keyFail(err)
	}
	return keySettingsOut(s), nil
}

func keySettingsOut(s keysettings.Settings) KeySettings {
	return KeySettings{AllowedModels: s.AllowedModels, Tags: s.Tags, Developer: s.Developer, RPM: s.RPM, Route: s.Route}
}

func keyID(raw string) (uuid.UUID, error) {
	id, err := ids.Decode(ids.APIKey, raw)
	if err != nil {
		return id, httpx.NotFound("API key")
	}
	return id, nil
}

func keyFail(err error) error {
	var bad *keysettings.InvalidError
	switch {
	case errors.As(err, &bad):
		return muzak.UnprocessableEntity(bad.Reason).WithCode(CodeInvalidKeySettings)
	case errors.Is(err, keysettings.ErrNotFound):
		return httpx.NotFound("API key")
	case errors.Is(err, keysettings.ErrScope):
		return muzak.UnprocessableEntity("the key lacks the gateway scope: issue one with it").WithCode(CodeKeyNotForGateway)
	}
	return err
}
