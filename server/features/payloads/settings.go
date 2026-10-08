// Package payloads keeps the payloads of the calls of projects that ask
// for them: the request as the provider got it and the answer the client
// got, secrets and personal data always masked, for as long as the plan
// keeps logs. Payloads are sealed per organization and packed in segments,
// objects of many payloads each, so storage costs a write per segment, not
// per call; a call's request-log row points at its payload.
package payloads

import (
	"context"
	"errors"
	"log/slog"
	"maps"

	"github.com/google/uuid"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/capturesetting"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
)

// Settings are whether a project keeps payloads, and which.
type Settings struct {
	Enabled bool `json:"enabled"`
	// SampleRate is the share of calls kept, 0 to 1.
	SampleRate float64 `json:"sample_rate"`
	// Answers keeps answers too, not only requests.
	Answers bool `json:"answers"`
}

// Defaults are a project's until it saves its own: off, and when turned
// on, every call with its answer.
func Defaults() Settings { return Settings{SampleRate: 1, Answers: true} }

// ErrInvalid is settings that can't be saved.
var ErrInvalid = errors.New("payloads: sample_rate is 0 to 1")

// Service keeps projects' capture settings.
type Service struct {
	ent     *pdent.Client
	changed func(ctx context.Context, project uuid.UUID)
}

// NewService returns a Service; changed runs after a project's settings
// change.
func NewService(ent *pdent.Client, changed func(ctx context.Context, project uuid.UUID)) *Service {
	if changed == nil {
		changed = func(context.Context, uuid.UUID) {}
	}
	return &Service{ent: ent, changed: changed}
}

// Get returns a project's settings, the defaults when it saved none.
func (s *Service) Get(ctx context.Context, project uuid.UUID) (Settings, error) {
	row, err := s.ent.CaptureSetting.Query().Where(capturesetting.ProjectID(project)).Only(ctx)
	if pdent.IsNotFound(err) {
		return Defaults(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	return Settings{Enabled: row.Enabled, SampleRate: row.SampleRate, Answers: row.Answers}, nil
}

// Put replaces a project's settings.
func (s *Service) Put(ctx context.Context, org, project uuid.UUID, in Settings) (Settings, error) {
	if in.SampleRate < 0 || in.SampleRate > 1 {
		return Settings{}, ErrInvalid
	}
	err := s.ent.CaptureSetting.Create().SetOrgID(org).SetProjectID(project).SetEnabled(in.Enabled).
		SetSampleRate(in.SampleRate).SetAnswers(in.Answers).
		OnConflictColumns(capturesetting.FieldProjectID).UpdateEnabled().UpdateSampleRate().UpdateAnswers().UpdateUpdatedAt().
		Exec(ctx)
	if err != nil {
		return Settings{}, err
	}
	s.changed(ctx, project)
	return in, nil
}

// Capture is what the gateway holds of a project that keeps payloads:
// its settings, and the engine that masks them.
type Capture struct {
	Settings
	// Mask redacts secrets, personal data (every detector) and the
	// project's own patterns.
	Mask *firewall.Engine
}

// Compile prepares a project's capture: nil when it keeps no payloads.
// custom are the firewall's patterns of the project, masked too.
func Compile(s Settings, custom map[string]string) *Capture {
	if !s.Enabled || s.SampleRate <= 0 {
		return nil
	}
	fw := firewall.Settings{Enabled: true, Secrets: firewall.Redact, PII: firewall.Redact, CustomAction: firewall.Redact,
		Custom: maps.Clone(custom), FailMode: firewall.FailOpen}
	e, err := firewall.Compile(fw, firewall.Env())
	if err != nil {
		// The patterns were checked when saved; without them, the rest
		// is masked still.
		slog.Error("payloads: a project's patterns don't compile; payloads are masked without them", "err", err)
		fw.Custom = nil
		if e, err = firewall.Compile(fw, firewall.Env()); err != nil {
			return nil
		}
	}
	return &Capture{Settings: s, Mask: e}
}
