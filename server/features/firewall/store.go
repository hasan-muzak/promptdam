package firewall

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/policy"

	"github.com/hasan-muzak/promptdam/server/catalog"
	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/firewallsetting"
)

// Env is the process's environment for policies: compiled programs are
// cached in it, by their text.
var Env = sync.OnceValue(func() *policy.Env {
	env, err := NewEnv()
	if err != nil {
		panic(err) // the environment is declared here, never from input
	}
	return env
})

// Needs are the plan features the changes from old to s need: masking or
// tokenizing personal data, a policy or a custom pattern old doesn't
// have. What old already has needs nothing: a downgrade never loosens a
// firewall, and keeping (or removing) what the plan no longer includes
// while changing something else isn't an error.
func (s Settings) Needs(old Settings) []string {
	var out []string
	if s.PII != old.PII && s.PII != Off && s.PII != Alert {
		out = append(out, catalog.PIIRedaction)
	}
	if slices.ContainsFunc(s.Policies, func(p Policy) bool { return !slices.Contains(old.Policies, p) }) {
		out = append(out, catalog.FirewallPolicies)
	}
	for name, re := range s.Custom {
		if was, ok := old.Custom[name]; !ok || was != re {
			out = append(out, catalog.CustomPolicies)
			break
		}
	}
	return out
}

// Service keeps projects' firewall settings.
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
	row, err := s.ent.FirewallSetting.Query().Where(firewallsetting.ProjectID(project)).Only(ctx)
	if pdent.IsNotFound(err) {
		return Defaults(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	if err := json.Unmarshal(row.Settings, &out); err != nil {
		return Settings{}, fmt.Errorf("firewall: project %s's settings: %w", project, err)
	}
	return out, nil
}

// Put checks and replaces a project's settings; the plan's features are
// the caller's to check (Needs).
func (s *Service) Put(ctx context.Context, org, project uuid.UUID, in Settings) (Settings, error) {
	if err := in.Check(Env()); err != nil {
		return Settings{}, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return Settings{}, err
	}
	err = s.ent.FirewallSetting.Create().SetOrgID(org).SetProjectID(project).SetSettings(raw).
		OnConflictColumns(firewallsetting.FieldProjectID).UpdateSettings().UpdateUpdatedAt().
		Exec(ctx)
	if err != nil {
		return Settings{}, err
	}
	s.changed(ctx, project)
	return in, nil
}

// Load compiles the settings a snapshot carries; settings that no longer
// compile (they were checked when saved) fall back to the defaults, which
// still block secrets.
func Load(s Settings) *Engine {
	e, err := Compile(s, Env())
	if err == nil {
		return e
	}
	slog.Error("firewall: a project's settings don't compile; the defaults apply", "err", err)
	e, _ = Compile(Defaults(), Env())
	return e
}
