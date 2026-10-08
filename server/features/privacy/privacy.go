// Package privacy keeps organizations' privacy: payload-free mode, the
// Business plan's guarantee that no prompt or answer is stored. While on,
// projects can't turn on what would keep content (payload capture,
// firewall excerpts, the response cache), and gateways keep none of it
// whatever was saved before; turning it on deletes what was kept.
package privacy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/entx"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/privacysetting"
	"github.com/hasan-muzak/promptdam/server/features/payloads"
	"github.com/hasan-muzak/promptdam/server/features/responsecache"
)

// Settings are an organization's privacy.
type Settings struct {
	PayloadFree bool
	UpdatedAt   time.Time
	UpdatedBy   string
}

// Config configures a Service.
type Config struct {
	Ent *pdent.Client
	// Payloads and Cache delete what was kept when payload-free mode turns
	// on (nil: nothing to delete).
	Payloads *payloads.Store
	Cache    *responsecache.Cache
	// Projects lists an organization's projects.
	Projects func(ctx context.Context, org uuid.UUID) ([]uuid.UUID, error)
	// Changed runs after a project's gateway configuration changes.
	Changed func(ctx context.Context, project uuid.UUID)
}

// Service keeps organizations' privacy.
type Service struct{ cfg Config }

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.Changed == nil {
		cfg.Changed = func(context.Context, uuid.UUID) {}
	}
	return &Service{cfg: cfg}
}

// Get returns an organization's privacy (payload-free mode off when it set
// none).
func (s *Service) Get(ctx context.Context, org uuid.UUID) (Settings, error) {
	row, err := s.cfg.Ent.PrivacySetting.Query().Where(privacysetting.OrgID(org)).Only(entx.WithOrg(ctx, org))
	if pdent.IsNotFound(err) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	return Settings{PayloadFree: row.PayloadFree, UpdatedAt: row.UpdatedAt, UpdatedBy: row.UpdatedBy}, nil
}

// PayloadFree reports whether an organization keeps no content; when it
// can't be read, it does keep none (the safe side).
func (s *Service) PayloadFree(ctx context.Context, org uuid.UUID) bool {
	st, err := s.Get(ctx, org)
	return err != nil || st.PayloadFree
}

// Put sets payload-free mode. Turning it on deletes what was kept: every
// project's payloads and cached answers.
func (s *Service) Put(ctx context.Context, org uuid.UUID, payloadFree bool, by string) (Settings, error) {
	ctx = entx.WithOrg(ctx, org)
	was, err := s.Get(ctx, org)
	if err != nil {
		return Settings{}, err
	}
	err = s.cfg.Ent.PrivacySetting.Create().SetOrgID(org).SetPayloadFree(payloadFree).SetUpdatedBy(by).
		OnConflictColumns(privacysetting.FieldOrgID).UpdatePayloadFree().UpdateUpdatedBy().UpdateUpdatedAt().
		Exec(ctx)
	if err != nil {
		return Settings{}, err
	}
	projects, err := s.cfg.Projects(ctx, org)
	if err != nil {
		return Settings{}, fmt.Errorf("privacy: the organization's projects: %w", err)
	}
	// Gateways read the mode in each project's snapshot.
	for _, p := range projects {
		s.cfg.Changed(ctx, p)
	}
	if payloadFree && !was.PayloadFree {
		if _, err := s.cfg.Payloads.DeleteOrg(ctx, org); err != nil {
			return Settings{}, fmt.Errorf("privacy: deleting the payloads kept: %w", err)
		}
		if s.cfg.Cache != nil && len(projects) > 0 {
			scopes := make([]string, len(projects))
			for i, p := range projects {
				scopes[i] = p.String()
			}
			if _, err := s.cfg.Cache.Forget(ctx, scopes...); err != nil {
				// They expire within their route's TTL; gateways no longer
				// read them.
				slog.Warn("privacy: cached answers weren't cleared", "org", org, "err", err)
			}
		}
	}
	return s.Get(ctx, org)
}
