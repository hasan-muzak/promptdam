// Package keysettings keeps what the gateway applies to each API key: the
// models it may call, its developer and tags, its rate limit and default
// route. A key without settings may call every model, without a limit.
package keysettings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/ent"
	"github.com/hasan-muzak/keel-core/pkg/entx"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/keysetting"
)

// Errors.
var (
	// ErrNotFound: the key isn't one of the project's live keys.
	ErrNotFound = errors.New("keysettings: no such key")
	// ErrScope: the key can't call the gateway.
	ErrScope = errors.New("keysettings: the key lacks the gateway scope")
)

// InvalidError is input that can't be saved; its message says why.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "keysettings: " + e.Reason }

// Settings are a key's gateway settings.
type Settings struct {
	AllowedModels []string
	Tags          []string
	Developer     string
	RPM           int
	Route         string
}

// Allows reports whether the settings let the key call model: any model
// when none are listed; a listed name ending in * matches a prefix.
func (s Settings) Allows(model string) bool {
	if len(s.AllowedModels) == 0 {
		return true
	}
	for _, m := range s.AllowedModels {
		if p, ok := strings.CutSuffix(m, "*"); ok && strings.HasPrefix(model, p) || m == model {
			return true
		}
	}
	return false
}

// KeyLookup finds a core API key (apikeys.Service.Get).
type KeyLookup func(ctx context.Context, org, id uuid.UUID) (*ent.APIKey, error)

// Config configures a Service.
type Config struct {
	Ent *pdent.Client
	Key KeyLookup
	// Scope is the scope a key needs to call the gateway.
	Scope string
	// Changed runs after a project's settings change.
	Changed func(ctx context.Context, project uuid.UUID)
}

// Service keeps key settings.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.Changed == nil {
		cfg.Changed = func(context.Context, uuid.UUID) {}
	}
	return &Service{cfg: cfg}
}

var (
	tagRE   = regexp.MustCompile(`^[A-Za-z0-9_.:=/-]{1,64}$`)
	routeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
)

func check(in *Settings) error {
	bad := func(format string, args ...any) error { return &InvalidError{Reason: fmt.Sprintf(format, args...)} }
	in.AllowedModels = clean(in.AllowedModels)
	in.Tags = clean(in.Tags)
	in.Developer = strings.TrimSpace(in.Developer)
	in.Route = strings.TrimSpace(in.Route)
	switch {
	case len(in.AllowedModels) > 200:
		return bad("a key lists at most 200 models")
	case len(in.Tags) > 20:
		return bad("a key has at most 20 tags")
	case len(in.Developer) > 200:
		return bad("the developer is at most 200 characters")
	case in.RPM < 0 || in.RPM > 1_000_000:
		return bad("rpm is 0 (no limit) to 1,000,000")
	case in.Route != "" && !routeRE.MatchString(in.Route):
		return bad("a route name is lowercase letters, digits, dots, dashes or underscores")
	}
	for _, m := range in.AllowedModels {
		if len(m) > 200 || strings.Count(m, "*") > 1 || (strings.Contains(m, "*") && !strings.HasSuffix(m, "*")) {
			return bad("%q: a model name is at most 200 characters, with * only at its end", m)
		}
	}
	for _, t := range in.Tags {
		if !tagRE.MatchString(t) {
			return bad("%q: a tag is 1 to 64 letters, digits or _.:=/-", t)
		}
	}
	return nil
}

// clean trims, drops empty and duplicate values, keeping their order.
func clean(vs []string) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func from(row *pdent.KeySetting) Settings {
	return Settings{AllowedModels: row.AllowedModels, Tags: row.Tags, Developer: row.Developer, RPM: row.Rpm, Route: row.Route}
}

// liveKey checks that key is a live key of the project that can call the
// gateway.
func (s *Service) liveKey(ctx context.Context, org, project, key uuid.UUID) error {
	k, err := s.cfg.Key(ctx, org, key)
	if err != nil || k.ProjectID == nil || *k.ProjectID != project || k.RevokedAt != nil {
		return ErrNotFound
	}
	if !slices.Contains(k.Scopes, s.cfg.Scope) {
		return ErrScope
	}
	return nil
}

// Get returns a key's settings (the defaults when it has none).
func (s *Service) Get(ctx context.Context, org, project, key uuid.UUID) (Settings, error) {
	if err := s.liveKey(ctx, org, project, key); err != nil {
		return Settings{}, err
	}
	row, err := s.cfg.Ent.KeySetting.Query().Where(keysetting.APIKeyID(key), keysetting.ProjectID(project)).Only(ctx)
	if pdent.IsNotFound(err) {
		return Settings{AllowedModels: []string{}, Tags: []string{}}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	return from(row), nil
}

// Put replaces a key's settings.
func (s *Service) Put(ctx context.Context, org, project, key uuid.UUID, in Settings) (Settings, error) {
	if err := check(&in); err != nil {
		return Settings{}, err
	}
	if err := s.liveKey(ctx, org, project, key); err != nil {
		return Settings{}, err
	}
	err := s.cfg.Ent.KeySetting.Create().
		SetOrgID(org).SetProjectID(project).SetAPIKeyID(key).
		SetAllowedModels(in.AllowedModels).SetTags(in.Tags).SetDeveloper(in.Developer).SetRpm(in.RPM).SetRoute(in.Route).
		OnConflictColumns(keysetting.FieldAPIKeyID).
		UpdateAllowedModels().UpdateTags().UpdateDeveloper().UpdateRpm().UpdateRoute().UpdateUpdatedAt().
		Exec(ctx)
	if err != nil {
		return Settings{}, err
	}
	s.cfg.Changed(ctx, project)
	return in, nil
}

// All returns every key's settings of a project, by key.
func (s *Service) All(ctx context.Context, project uuid.UUID) (map[uuid.UUID]Settings, error) {
	rows, err := s.cfg.Ent.KeySetting.Query().Where(keysetting.ProjectID(project)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]Settings, len(rows))
	for _, r := range rows {
		out[r.APIKeyID] = from(r)
	}
	return out, nil
}

// Rotated copies a rotated key's settings to its replacement
// (apikeys.Service.OnRotated). The old key keeps its own until it expires.
func (s *Service) Rotated(ctx context.Context, org, old, replacement uuid.UUID) error {
	ctx = entx.WithOrg(ctx, org)
	row, err := s.cfg.Ent.KeySetting.Query().Where(keysetting.APIKeyID(old)).Only(ctx)
	if pdent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	err = s.cfg.Ent.KeySetting.Create().
		SetOrgID(org).SetProjectID(row.ProjectID).SetAPIKeyID(replacement).
		SetAllowedModels(row.AllowedModels).SetTags(row.Tags).SetDeveloper(row.Developer).SetRpm(row.Rpm).SetRoute(row.Route).
		OnConflictColumns(keysetting.FieldAPIKeyID).DoNothing().
		Exec(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	s.cfg.Changed(ctx, row.ProjectID)
	return nil
}
