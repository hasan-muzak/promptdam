// Package providers keeps the LLM providers each project calls through the
// gateway with its own keys (BYOK). Keys are sealed with pkg/vault, never
// returned, and stay bound to the endpoint they were given for.
package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/safehttp"
	"github.com/hasan-muzak/keel-core/pkg/vault"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/provider"
	"github.com/hasan-muzak/promptdam/server/features/cloudauth"
)

// MaxPerProject bounds a project's providers: the gateway holds them all
// in memory.
const MaxPerProject = 50

// purpose binds sealed keys to their use (vault additional data).
const purpose = "promptdam.provider.key"

// Errors.
var (
	ErrNotFound = errors.New("providers: not found")
	// ErrNameTaken: another provider of the project has the name.
	ErrNameTaken = errors.New("providers: the name is taken")
	// ErrLimit: the project has MaxPerProject providers.
	ErrLimit = errors.New("providers: too many providers")
	// ErrKeyRequired: a key is needed (a new provider, or a stored key
	// moved to another endpoint).
	ErrKeyRequired = errors.New("providers: an API key is required")
)

// InvalidError is input that can't be saved; its message says why.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "providers: " + e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidError{Reason: fmt.Sprintf(format, args...)}
}

// Config configures a Service.
type Config struct {
	Ent   *pdent.Client
	Vault *vault.Vault
	// HTTP checks keys against providers (safehttp.NewClient).
	HTTP *http.Client
	// AllowLoopback lets base URLs point at this machine, over plain HTTP
	// too: development only.
	AllowLoopback bool
	// Changed runs after a project's providers change, so the gateway
	// reloads them.
	Changed func(ctx context.Context, project uuid.UUID)
	// GoogleTokenURL is where Vertex providers' service-account keys get
	// their tokens (cloudauth.GoogleTokenURL; a fake in tests).
	GoogleTokenURL string
}

// Service keeps providers.
type Service struct {
	cfg    Config
	tokens *cloudauth.Tokens
}

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.HTTP == nil {
		cfg.HTTP = safehttp.NewClient(safehttp.Options{AllowLoopback: cfg.AllowLoopback})
	}
	if cfg.Changed == nil {
		cfg.Changed = func(context.Context, uuid.UUID) {}
	}
	if cfg.GoogleTokenURL == "" {
		cfg.GoogleTokenURL = cloudauth.GoogleTokenURL
	}
	return &Service{cfg: cfg, tokens: cloudauth.NewTokens(cfg.HTTP, cfg.GoogleTokenURL)}
}

// Input is a provider's fields.
type Input struct {
	Name string
	Kind string
	// Format is the API a custom provider speaks; known kinds have theirs.
	Format string
	// BaseURL overrides a known kind's (a regional endpoint); custom
	// providers need one. A cloud's is built from Cloud (in development and
	// tests, with loopback allowed, one given wins: a fake cloud).
	BaseURL string
	// Cloud is where a cloud kind's models are.
	Cloud Cloud
	// APIKey is the provider key; nil keeps the stored one on update. A
	// cloud that takes a key (Bedrock, Azure) may take it here too.
	APIKey *string
	// Credential is how a cloud kind signs in; nil keeps the stored one on
	// update.
	Credential *Credential
	Models     []string
	Priority   int
	Enabled    bool
}

// secret is the input's secret as it is sealed, its hint, and whether one
// was given (nil and empty keep or need the stored one).
func (in *Input) secret() (secret []byte, hint string, err error) {
	if IsCloud(in.Kind) {
		if in.Credential == nil {
			return nil, "", nil
		}
		hint, err := in.Credential.check(in.Kind)
		if err != nil {
			return nil, "", err
		}
		return in.Credential.sealable(), hint, nil
	}
	if in.APIKey == nil || *in.APIKey == "" {
		return nil, "", nil
	}
	return []byte(*in.APIKey), lastChars(*in.APIKey), nil
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

// normalize checks in and fills its defaults.
func (s *Service) normalize(ctx context.Context, in *Input) error {
	in.Name = strings.TrimSpace(in.Name)
	if !nameRE.MatchString(in.Name) {
		return invalid("a name is 1 to 100 lowercase letters, digits, dots, dashes or underscores, starting with a letter or digit")
	}
	kind, ok := LookupKind(in.Kind)
	if !ok {
		return invalid("unknown kind %q", in.Kind)
	}
	switch {
	case kind.Name == Custom:
		if in.Format != FormatOpenAI && in.Format != FormatAnthropic {
			return invalid("a custom provider speaks the openai or the anthropic API")
		}
		if in.BaseURL == "" {
			return invalid("a custom provider needs its base URL")
		}
	case kind.Cloud:
		in.Format = kind.Format
		if in.APIKey != nil && *in.APIKey != "" && in.Credential == nil {
			in.Credential = &Credential{APIKey: strings.TrimSpace(*in.APIKey)}
		}
		in.APIKey = nil
		if in.Credential != nil && len(in.Credential.ServiceAccount) <= maxServiceAccount {
			// A Vertex project defaults to the service account's.
			var sa struct {
				ProjectID string `json:"project_id"`
			}
			if in.Cloud.Project == "" && json.Unmarshal([]byte(in.Credential.ServiceAccount), &sa) == nil {
				in.Cloud.Project = sa.ProjectID
			}
		}
		if in.BaseURL == "" || !s.cfg.AllowLoopback {
			base, err := CloudBaseURL(kind.Name, in.Cloud)
			if err != nil {
				return err
			}
			in.BaseURL = base
		}
	default:
		in.Format = kind.Format
		if in.BaseURL == "" {
			in.BaseURL = kind.BaseURL
		}
	}
	base, err := s.checkURL(ctx, in.BaseURL)
	if err != nil {
		return err
	}
	in.BaseURL = base
	models := make([]string, 0, len(in.Models))
	seen := map[string]bool{}
	for _, m := range in.Models {
		m = strings.TrimSpace(m)
		if m == "" || len(m) > 200 {
			return invalid("a model name is 1 to 200 characters")
		}
		if !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}
	if len(models) > 500 {
		return invalid("a provider lists at most 500 models")
	}
	in.Models = models
	if in.APIKey != nil {
		key := strings.TrimSpace(*in.APIKey)
		if len(key) > 4096 {
			return invalid("the API key is too long")
		}
		in.APIKey = &key
	}
	if in.Credential != nil && !kind.Cloud {
		return invalid("only a cloud provider takes a credential; this kind takes an API key")
	}
	return nil
}

// checkURL accepts an https URL (http only with AllowLoopback) without
// credentials, query or fragment, whose host isn't internal.
func (s *Service) checkURL(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", invalid("the base URL must be an absolute URL without credentials, query or fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !s.cfg.AllowLoopback) {
		return "", invalid("the base URL must use https")
	}
	if err := safehttp.CheckURLHost(ctx, u.Hostname(), s.cfg.AllowLoopback); err != nil {
		if errors.Is(err, safehttp.ErrBlocked) {
			return "", invalid("the base URL must be a public address")
		}
		return "", invalid("the base URL's host can't be resolved")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// List returns a project's providers, by priority then name.
func (s *Service) List(ctx context.Context, project uuid.UUID) ([]*pdent.Provider, error) {
	return s.cfg.Ent.Provider.Query().Where(provider.ProjectID(project)).
		Order(pdent.Asc(provider.FieldPriority), pdent.Asc(provider.FieldName)).All(ctx)
}

// Get returns one of a project's providers.
func (s *Service) Get(ctx context.Context, project, id uuid.UUID) (*pdent.Provider, error) {
	p, err := s.cfg.Ent.Provider.Query().Where(provider.ID(id), provider.ProjectID(project)).Only(ctx)
	if pdent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return p, err
}

// Create adds a provider to a project.
func (s *Service) Create(ctx context.Context, org, project uuid.UUID, in Input) (*pdent.Provider, error) {
	if err := s.normalize(ctx, &in); err != nil {
		return nil, err
	}
	secret, hint, err := in.secret()
	if err != nil {
		return nil, err
	}
	if secret == nil && in.Kind != Custom {
		return nil, ErrKeyRequired
	}
	n, err := s.cfg.Ent.Provider.Query().Where(provider.ProjectID(project)).Count(ctx)
	if err != nil {
		return nil, err
	}
	if n >= MaxPerProject {
		return nil, ErrLimit
	}
	sealed, err := s.seal(org, secret)
	if err != nil {
		return nil, err
	}
	p, err := s.cfg.Ent.Provider.Create().
		SetOrgID(org).SetProjectID(project).
		SetName(in.Name).SetKind(in.Kind).SetAPIFormat(provider.APIFormat(in.Format)).SetBaseURL(in.BaseURL).
		SetSecrets(sealed).SetSecretHint(hint).SetModels(in.Models).SetPriority(in.Priority).SetEnabled(in.Enabled).
		Save(ctx)
	if pdent.IsConstraintError(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, err
	}
	s.cfg.Changed(ctx, project)
	return p, nil
}

// Update replaces a provider's fields. Its stored key goes only to the
// endpoint it was given for: a new base URL or kind needs the key again,
// so a key can't be sent to a server of the caller's choosing.
func (s *Service) Update(ctx context.Context, org, project, id uuid.UUID, in Input) (*pdent.Provider, error) {
	cur, err := s.Get(ctx, project, id)
	if err != nil {
		return nil, err
	}
	if err := s.normalize(ctx, &in); err != nil {
		return nil, err
	}
	moved := in.BaseURL != cur.BaseURL || in.Kind != cur.Kind
	u := s.cfg.Ent.Provider.UpdateOne(cur).
		SetName(in.Name).SetKind(in.Kind).SetAPIFormat(provider.APIFormat(in.Format)).SetBaseURL(in.BaseURL).
		SetModels(in.Models).SetPriority(in.Priority).SetEnabled(in.Enabled)
	secret, hint, err := in.secret()
	if err != nil {
		return nil, err
	}
	switch {
	case in.APIKey != nil || in.Credential != nil:
		if secret == nil && in.Kind != Custom {
			return nil, ErrKeyRequired
		}
		sealed, err := s.seal(org, secret)
		if err != nil {
			return nil, err
		}
		u.SetSecrets(sealed).SetSecretHint(hint).ClearCheckedAt().SetCheckError("")
	case moved && cur.Secrets != "":
		return nil, ErrKeyRequired
	}
	p, err := u.Save(ctx)
	if pdent.IsConstraintError(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, err
	}
	s.cfg.Changed(ctx, project)
	return p, nil
}

// Delete removes a provider.
func (s *Service) Delete(ctx context.Context, project, id uuid.UUID) error {
	n, err := s.cfg.Ent.Provider.Delete().Where(provider.ID(id), provider.ProjectID(project)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.cfg.Changed(ctx, project)
	return nil
}

// seal seals a secret: "" for none.
func (s *Service) seal(org uuid.UUID, secret []byte) (string, error) {
	if len(secret) == 0 {
		return "", nil
	}
	sealed, err := s.cfg.Vault.Seal(org, purpose, secret)
	if err != nil {
		return "", fmt.Errorf("providers: sealing the key: %w", err)
	}
	return sealed, nil
}

// Auth opens a sealed secret of org's into what signs a provider's calls:
// fixed headers for a key (HeaderFor; Azure's api-key), or, for a cloud
// that signs each request, an authorizer (Bedrock's API key or SigV4,
// Vertex's tokens, kept across snapshots by the sealed secret).
func (s *Service) Auth(org uuid.UUID, kind, format, sealed string) (http.Header, cloudauth.Authorizer, error) {
	if !IsCloud(kind) {
		h, err := s.HeaderFor(org, format, sealed)
		return h, nil, err
	}
	if sealed == "" {
		return http.Header{}, nil, nil
	}
	raw, err := s.cfg.Vault.Open(org, purpose, sealed)
	if err != nil {
		return nil, nil, fmt.Errorf("providers: opening a key: %w", err)
	}
	var c Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, nil, fmt.Errorf("providers: reading a cloud credential: %w", err)
	}
	switch {
	case kind == Azure:
		return http.Header{"Api-Key": {c.APIKey}}, nil, nil
	case kind == Bedrock && c.APIKey != "":
		return http.Header{}, cloudauth.BedrockKey(c.APIKey), nil
	case kind == Bedrock:
		return http.Header{}, cloudauth.SigV4(c.AccessKeyID, c.SecretAccessKey, c.SessionToken), nil
	}
	sa, err := cloudauth.ParseServiceAccount([]byte(c.ServiceAccount))
	if err != nil {
		return nil, nil, fmt.Errorf("providers: reading a service-account key: %w", err)
	}
	sum := sha256.Sum256([]byte(sealed))
	return http.Header{}, s.tokens.Google(hex.EncodeToString(sum[:]), sa), nil
}

// Header returns the headers that carry a provider's key.
func (s *Service) Header(p *pdent.Provider) (http.Header, error) {
	return s.HeaderFor(p.OrgID, string(p.APIFormat), p.Secrets)
}

// HeaderFor returns the headers that carry a sealed key of org's: a bearer
// token for the OpenAI API, x-api-key for Anthropic's. No key, no headers.
func (s *Service) HeaderFor(org uuid.UUID, format, sealed string) (http.Header, error) {
	h := http.Header{}
	if sealed == "" {
		return h, nil
	}
	key, err := s.cfg.Vault.Open(org, purpose, sealed)
	if err != nil {
		return nil, fmt.Errorf("providers: opening a key: %w", err)
	}
	if format == FormatAnthropic {
		h.Set("X-Api-Key", string(key))
	} else {
		h.Set("Authorization", "Bearer "+string(key))
	}
	return h, nil
}
