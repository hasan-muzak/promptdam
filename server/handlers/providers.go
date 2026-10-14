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

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/features/providers"
)

// ProviderPrefix starts provider IDs: prv_….
const ProviderPrefix = "prv"

// Error codes of the provider routes.
const (
	CodeInvalidProvider   = "invalid_provider"
	CodeProviderNameTaken = "provider_name_taken"
	CodeProviderLimit     = "provider_limit"
	CodeAPIKeyRequired    = "api_key_required"
)

// ProviderKindsOut lists the providers Promptdam knows.
type ProviderKindsOut struct {
	Data []providers.Kind `json:"data"`
}

// ProviderKinds lists the kinds of provider a project can add, with the
// API each speaks and its default base URL; a cloud's (cloud: true) comes
// from where its account is (region, project and location, resource).
func (h *Handlers) ProviderKinds(*muzak.Context, muzak.Empty) (ProviderKindsOut, error) {
	return ProviderKindsOut{Data: providers.Kinds}, nil
}

// ProviderOut is a provider. Its key is never returned, only whether one is
// set and its last characters.
type ProviderOut struct {
	ID        string `json:"id" doc:"Provider ID (prv_…)"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	APIFormat string `json:"api_format" doc:"The API it speaks: openai or anthropic"`
	BaseURL   string `json:"base_url"`
	// Where a cloud provider's account is.
	Region   string   `json:"region,omitempty" doc:"Bedrock: the AWS region"`
	Project  string   `json:"project,omitempty" doc:"Vertex: the Google Cloud project"`
	Location string   `json:"location,omitempty" doc:"Vertex: global, eu, us or a region"`
	Resource string   `json:"resource,omitempty" doc:"Azure: the AI Foundry resource"`
	KeySet   bool     `json:"key_set"`
	KeyHint  string   `json:"key_hint" doc:"The key's last characters (…a1b2), or a service account's email; empty for short keys"`
	Models   []string `json:"models" doc:"Models it serves; empty: the catalog's models of its kind (any model for custom)"`
	Priority int      `json:"priority" doc:"Lower first among providers serving the same model"`
	Enabled  bool     `json:"enabled"`
	// The last key check.
	CheckedAt  *time.Time `json:"checked_at"`
	CheckError string     `json:"check_error" doc:"Why the last check failed; empty when it passed"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func providerOut(p *pdent.Provider) ProviderOut {
	cloud := providers.CloudOf(p.Kind, p.BaseURL)
	return ProviderOut{
		ID: ids.Encode(ProviderPrefix, p.ID), Name: p.Name, Kind: p.Kind, APIFormat: string(p.APIFormat), BaseURL: p.BaseURL,
		Region: cloud.Region, Project: cloud.Project, Location: cloud.Location, Resource: cloud.Resource,
		KeySet: p.Secrets != "", KeyHint: p.SecretHint, Models: p.Models, Priority: p.Priority, Enabled: p.Enabled,
		CheckedAt: p.CheckedAt, CheckError: p.CheckError, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// CredentialIn is how a cloud provider signs in, write-only: Bedrock an API
// key or IAM access keys, Vertex a service-account key, Azure an API key.
type CredentialIn struct {
	APIKey          string `json:"api_key,omitempty" doc:"Bedrock or Azure: the API key"`
	AccessKeyID     string `json:"access_key_id,omitempty" doc:"Bedrock: an IAM access key id, signed with SigV4"`
	SecretAccessKey string `json:"secret_access_key,omitempty" doc:"Bedrock: the access key's secret"`
	SessionToken    string `json:"session_token,omitempty" doc:"Bedrock: a session token, for temporary keys"`
	ServiceAccount  string `json:"service_account,omitempty" doc:"Vertex: the service-account key file's contents (JSON)"`
}

// ProviderIn is a provider's fields.
type ProviderIn struct {
	Name       string        `json:"name" doc:"Unique in the project: lowercase letters, digits, dots, dashes, underscores"`
	Kind       string        `json:"kind" doc:"openai, anthropic, gemini, … or custom (GET /v1/provider-kinds)"`
	APIFormat  string        `json:"api_format,omitempty" doc:"openai or anthropic: custom providers only"`
	BaseURL    string        `json:"base_url,omitempty" doc:"Overrides the kind's (a regional endpoint); required for custom; a cloud's comes from region, project and location, or resource"`
	Region     string        `json:"region,omitempty" doc:"Bedrock: the AWS region, such as eu-west-1"`
	Project    string        `json:"project,omitempty" doc:"Vertex: the Google Cloud project id (default: the service account's)"`
	Location   string        `json:"location,omitempty" doc:"Vertex: global (default), eu, us or a region such as europe-west4"`
	Resource   string        `json:"resource,omitempty" doc:"Azure: the AI Foundry resource name ({resource}.services.ai.azure.com)"`
	APIKey     *string       `json:"api_key,omitempty" doc:"Write-only. On update, omit it to keep the stored key; a new base URL or kind needs it again"`
	Credential *CredentialIn `json:"credential,omitempty" doc:"Write-only, cloud providers: how they sign in. On update, omit it to keep the stored one"`
	Models     []string      `json:"models,omitempty" doc:"Models it serves; omit for the catalog's models of its kind"`
	Priority   int           `json:"priority,omitempty"`
	Enabled    *bool         `json:"enabled,omitempty" doc:"Default true"`
}

func (in *ProviderIn) validate(v *muzak.Validation) {
	v.String(&in.Name).Trim().Required().MaxLen(100)
	v.String(&in.Kind).Trim().Required().MaxLen(32)
	v.String(&in.BaseURL).Trim().MaxLen(500)
	v.String(&in.Region).Trim().MaxLen(40)
	v.String(&in.Project).Trim().MaxLen(40)
	v.String(&in.Location).Trim().MaxLen(40)
	v.String(&in.Resource).Trim().MaxLen(80)
	v.Slice(&in.Models).MaxItems(500)
	v.Number(&in.Priority).Min(-1000).Max(1000)
}

func (in ProviderIn) input() providers.Input {
	enabled := in.Enabled == nil || *in.Enabled
	var cred *providers.Credential
	if c := in.Credential; c != nil {
		cred = &providers.Credential{APIKey: c.APIKey, AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey,
			SessionToken: c.SessionToken, ServiceAccount: c.ServiceAccount}
	}
	return providers.Input{
		Name: in.Name, Kind: in.Kind, Format: in.APIFormat, BaseURL: in.BaseURL, APIKey: in.APIKey, Credential: cred,
		Cloud:  providers.Cloud{Region: in.Region, Project: in.Project, Location: in.Location, Resource: in.Resource},
		Models: in.Models, Priority: in.Priority, Enabled: enabled,
	}
}

// CreateProviderIn adds a provider.
type CreateProviderIn struct {
	platform.ProjectPath
	ProviderIn
}

// Validate implements muzak.Validatable.
func (in *CreateProviderIn) Validate(v *muzak.Validation) { in.validate(v) }

// ProviderPath names a provider.
type ProviderPath struct {
	platform.ProjectPath
	Provider string `path:"provider" doc:"Provider ID (prv_…)"`
}

// UpdateProviderIn replaces a provider.
type UpdateProviderIn struct {
	ProviderPath
	ProviderIn
}

// Validate implements muzak.Validatable.
func (in *UpdateProviderIn) Validate(v *muzak.Validation) { in.validate(v) }

// ListProviders lists a project's providers.
func (h *Handlers) ListProviders(ctx *muzak.Context, _ platform.ProjectPath) (httpx.List[ProviderOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	ps, err := h.Providers.List(ctx.Context(), pa.Project.ID)
	if err != nil {
		return httpx.List[ProviderOut]{}, err
	}
	out := make([]ProviderOut, len(ps))
	for i, p := range ps {
		out[i] = providerOut(p)
	}
	return httpx.List[ProviderOut]{Data: out}, nil
}

// CreateProvider adds a provider to a project.
func (h *Handlers) CreateProvider(ctx *muzak.Context, in CreateProviderIn) (ProviderOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	p, err := h.Providers.Create(ctx.Context(), pa.OrgID(), pa.Project.ID, in.input())
	if err != nil {
		return ProviderOut{}, providerFail(err)
	}
	h.audited(ctx, "provider.created", "provider", ids.Encode(ProviderPrefix, p.ID), map[string]any{"name": p.Name, "kind": p.Kind, "base_url": p.BaseURL})
	return providerOut(p), nil
}

// GetProvider returns a provider.
func (h *Handlers) GetProvider(ctx *muzak.Context, in ProviderPath) (ProviderOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := providerID(in.Provider)
	if err != nil {
		return ProviderOut{}, err
	}
	p, err := h.Providers.Get(ctx.Context(), pa.Project.ID, id)
	if err != nil {
		return ProviderOut{}, providerFail(err)
	}
	return providerOut(p), nil
}

// UpdateProvider replaces a provider's fields.
func (h *Handlers) UpdateProvider(ctx *muzak.Context, in UpdateProviderIn) (ProviderOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := providerID(in.Provider)
	if err != nil {
		return ProviderOut{}, err
	}
	p, err := h.Providers.Update(ctx.Context(), pa.OrgID(), pa.Project.ID, id, in.input())
	if err != nil {
		return ProviderOut{}, providerFail(err)
	}
	h.audited(ctx, "provider.updated", "provider", ids.Encode(ProviderPrefix, p.ID), map[string]any{"name": p.Name, "kind": p.Kind, "base_url": p.BaseURL, "enabled": p.Enabled, "key_replaced": in.APIKey != nil})
	return providerOut(p), nil
}

// DeleteProvider removes a provider.
func (h *Handlers) DeleteProvider(ctx *muzak.Context, in ProviderPath) (muzak.Empty, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := providerID(in.Provider)
	if err != nil {
		return muzak.Empty{}, err
	}
	if err := h.Providers.Delete(ctx.Context(), pa.Project.ID, id); err != nil {
		return muzak.Empty{}, providerFail(err)
	}
	h.audited(ctx, "provider.deleted", "provider", in.Provider, map[string]any{})
	return muzak.Empty{}, nil
}

// CheckProvider checks a provider's key against the provider.
func (h *Handlers) CheckProvider(ctx *muzak.Context, in ProviderPath) (ProviderOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := providerID(in.Provider)
	if err != nil {
		return ProviderOut{}, err
	}
	p, err := h.Providers.Check(ctx.Context(), pa.Project.ID, id)
	if err != nil {
		return ProviderOut{}, providerFail(err)
	}
	return providerOut(p), nil
}

func providerID(raw string) (uuid.UUID, error) {
	id, err := ids.Decode(ProviderPrefix, raw)
	if err != nil {
		return id, httpx.NotFound("provider")
	}
	return id, nil
}

func providerFail(err error) error {
	var bad *providers.InvalidError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return muzak.UnprocessableEntity(bad.Reason).WithCode(CodeInvalidProvider)
	case errors.Is(err, providers.ErrNotFound):
		return httpx.NotFound("provider")
	case errors.Is(err, providers.ErrNameTaken):
		return muzak.Conflict("another provider of the project has this name").WithCode(CodeProviderNameTaken)
	case errors.Is(err, providers.ErrLimit):
		return muzak.Conflict("a project has at most 50 providers").WithCode(CodeProviderLimit)
	case errors.Is(err, providers.ErrKeyRequired):
		return muzak.UnprocessableEntity("the provider's API key is required: a new provider, or a stored key moved to another endpoint").
			WithCode(CodeAPIKeyRequired)
	}
	return err
}
