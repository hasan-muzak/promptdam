// Package alerts keeps a project's alert rules and evaluates them: spend
// that spikes above its usual hour, provider errors over a window, a
// provider that looks down, slow answers. Budgets cap spend; alert rules
// say when something is going wrong before it costs or breaks much.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"

	"github.com/hasan-muzak/keel-core/pkg/kv"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/alertrule"
)

// IDPrefix starts alert rule IDs at the API: alr_….
const IDPrefix = "alr"

// Kinds of rule.
const (
	SpendSpike   = "spend_spike"
	ErrorRate    = "error_rate"
	ProviderDown = "provider_down"
	Latency      = "latency"
)

// Kinds are every kind of rule.
var Kinds = []string{SpendSpike, ErrorRate, ProviderDown, Latency}

// MaxPerProject bounds a project's rules.
const MaxPerProject = 20

// Windows a rule may read.
var Windows = []int{5, 15, 30, 60}

// Errors.
var (
	ErrNotFound  = errors.New("alerts: no such rule")
	ErrNameTaken = errors.New("alerts: another rule has this name")
	ErrLimit     = errors.New("alerts: a project has at most 20 rules")
)

// InvalidError is a rule that can't be saved; its message says why.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "alerts: " + e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidError{Reason: fmt.Sprintf(format, args...)}
}

// Input is a rule as it is saved. Zero values take the kind's defaults.
type Input struct {
	Name, Kind string
	// Threshold: spend_spike, times the usual hour (default 3); error_rate
	// and provider_down, the share of calls failing at the provider
	// (default 0.1 and 0.5); latency, the p95 in ms (default 10,000).
	Threshold     float64
	WindowMinutes int
	MinCalls      int
	// MinMicros is the least an hour must cost to be a spike (default $5).
	MinMicros       int64
	Provider, Model string
	Channels        []uuid.UUID
	Enabled         bool
	CooldownMinutes int
}

// Defaults fills the kind's defaults into zero values.
func (in *Input) Defaults() {
	type defaults struct {
		threshold float64
		window    int
		minCalls  int
	}
	d := map[string]defaults{
		SpendSpike: {3, 60, 0}, ErrorRate: {0.1, 15, 20}, ProviderDown: {0.5, 5, 5}, Latency: {10_000, 15, 20},
	}[in.Kind]
	if in.Threshold == 0 {
		in.Threshold = d.threshold
	}
	if in.WindowMinutes == 0 {
		in.WindowMinutes = d.window
	}
	if in.MinCalls == 0 {
		in.MinCalls = d.minCalls
	}
	if in.Kind == SpendSpike && in.MinMicros == 0 {
		in.MinMicros = 5_000_000
	}
	if in.CooldownMinutes == 0 {
		in.CooldownMinutes = 60
	}
}

// Check validates a rule, with its defaults filled.
func (in *Input) Check() error {
	in.Name, in.Provider, in.Model = strings.TrimSpace(in.Name), strings.TrimSpace(in.Provider), strings.TrimSpace(in.Model)
	if !slices.Contains(Kinds, in.Kind) {
		return invalid("kind is spend_spike, error_rate, provider_down or latency")
	}
	in.Defaults()
	switch {
	case in.Name == "" || len(in.Name) > 100:
		return invalid("a name is 1 to 100 characters")
	case !slices.Contains(Windows, in.WindowMinutes):
		return invalid("window_minutes is 5, 15, 30 or 60")
	case in.Kind == SpendSpike && in.WindowMinutes != 60:
		return invalid("a spend spike compares the last hour with the usual one: window_minutes is 60")
	case in.Kind == SpendSpike && (in.Threshold < 1.1 || in.Threshold > 100):
		return invalid("a spend spike's threshold is 1.1 to 100 times the usual hour")
	case (in.Kind == ErrorRate || in.Kind == ProviderDown) && (in.Threshold <= 0 || in.Threshold > 1):
		return invalid("an error share is above 0 and at most 1 (0.1 is 10%%)")
	case in.Kind == Latency && (in.Threshold < 100 || in.Threshold > 600_000):
		return invalid("a latency threshold is 100 to 600,000 ms")
	case in.MinCalls < 0 || in.MinCalls > 1_000_000:
		return invalid("min_calls is 0 to 1,000,000")
	case in.MinMicros < 0:
		return invalid("min_micros is 0 or more")
	case in.Kind != SpendSpike && in.MinMicros != 0:
		return invalid("min_micros is a spend spike's only")
	case len(in.Provider) > 100 || len(in.Model) > 200:
		return invalid("a provider's name is at most 100 characters, a model's 200")
	case in.CooldownMinutes < 5 || in.CooldownMinutes > 7*24*60:
		return invalid("cooldown_minutes is 5 to 10,080 (a week)")
	case len(in.Channels) > 20:
		return invalid("a rule alerts at most 20 channels")
	}
	return nil
}

// Service keeps alert rules.
type Service struct {
	ent *pdent.Client
	kv  *kv.Store
}

// New returns a Service; kv holds what evaluations measured.
func New(ent *pdent.Client, store *kv.Store) *Service { return &Service{ent: ent, kv: store} }

// Live is what a rule's last evaluation measured, in its threshold's unit.
type Live struct {
	Value float64   `json:"value"`
	At    time.Time `json:"at"`
}

// liveTTL drops a measure no evaluation renewed: a rule the plan stopped
// evaluating has none.
const liveTTL = 10 * time.Minute

func liveKey(store *kv.Store, rule uuid.UUID) string { return store.Key("alerts", "live", rule.String()) }

// saveLive keeps the rules' measures, in one round trip.
func saveLive(ctx context.Context, store *kv.Store, live map[uuid.UUID]Live) error {
	if store == nil || len(live) == 0 {
		return nil
	}
	c := store.Client
	cmds := make(valkey.Commands, 0, len(live))
	for id, l := range live {
		raw, _ := json.Marshal(l)
		cmds = append(cmds, c.B().Set().Key(liveKey(store, id)).Value(string(raw)).Ex(liveTTL).Build())
	}
	for _, r := range c.DoMulti(ctx, cmds...) {
		if err := r.Error(); err != nil {
			return err
		}
	}
	return nil
}

// Live returns what the rules' last evaluations measured; rules not
// evaluated lately are missing.
func (s *Service) Live(ctx context.Context, rules ...uuid.UUID) map[uuid.UUID]Live {
	out := map[uuid.UUID]Live{}
	if s.kv == nil || len(rules) == 0 {
		return out
	}
	keys := make([]string, len(rules))
	for i, id := range rules {
		keys[i] = liveKey(s.kv, id)
	}
	c := s.kv.Client
	vals, err := c.Do(ctx, c.B().Mget().Key(keys...).Build()).ToArray()
	if err != nil {
		return out
	}
	for i, v := range vals {
		raw, err := v.ToString()
		if err != nil {
			continue
		}
		var l Live
		if json.Unmarshal([]byte(raw), &l) == nil {
			out[rules[i]] = l
		}
	}
	return out
}

// List returns a project's rules, by name.
func (s *Service) List(ctx context.Context, project uuid.UUID) ([]*pdent.AlertRule, error) {
	return s.ent.AlertRule.Query().Where(alertrule.ProjectID(project)).Order(pdent.Asc(alertrule.FieldName)).All(ctx)
}

// Get returns a rule of a project.
func (s *Service) Get(ctx context.Context, project, id uuid.UUID) (*pdent.AlertRule, error) {
	r, err := s.ent.AlertRule.Query().Where(alertrule.ID(id), alertrule.ProjectID(project)).Only(ctx)
	if pdent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return r, err
}

func channelIDs(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// Create adds a rule.
func (s *Service) Create(ctx context.Context, org, project uuid.UUID, in Input) (*pdent.AlertRule, error) {
	if err := in.Check(); err != nil {
		return nil, err
	}
	n, err := s.ent.AlertRule.Query().Where(alertrule.ProjectID(project)).Count(ctx)
	if err != nil {
		return nil, err
	}
	if n >= MaxPerProject {
		return nil, ErrLimit
	}
	r, err := s.ent.AlertRule.Create().SetOrgID(org).SetProjectID(project).SetName(in.Name).SetKind(alertrule.Kind(in.Kind)).
		SetThreshold(in.Threshold).SetWindowMinutes(in.WindowMinutes).SetMinCalls(in.MinCalls).SetMinMicros(in.MinMicros).
		SetProvider(in.Provider).SetModel(in.Model).SetChannelIds(channelIDs(in.Channels)).SetEnabled(in.Enabled).
		SetCooldownMinutes(in.CooldownMinutes).Save(ctx)
	if pdent.IsConstraintError(err) {
		return nil, ErrNameTaken
	}
	return r, err
}

// Update replaces a rule. A changed condition starts it anew: not firing.
func (s *Service) Update(ctx context.Context, project, id uuid.UUID, in Input) (*pdent.AlertRule, error) {
	if err := in.Check(); err != nil {
		return nil, err
	}
	old, err := s.Get(ctx, project, id)
	if err != nil {
		return nil, err
	}
	u := s.ent.AlertRule.UpdateOne(old).SetName(in.Name).SetKind(alertrule.Kind(in.Kind)).SetThreshold(in.Threshold).
		SetWindowMinutes(in.WindowMinutes).SetMinCalls(in.MinCalls).SetMinMicros(in.MinMicros).SetProvider(in.Provider).
		SetModel(in.Model).SetChannelIds(channelIDs(in.Channels)).SetEnabled(in.Enabled).SetCooldownMinutes(in.CooldownMinutes)
	if string(old.Kind) != in.Kind || old.Threshold != in.Threshold || old.WindowMinutes != in.WindowMinutes ||
		old.Provider != in.Provider || old.Model != in.Model || old.Enabled != in.Enabled {
		u.SetFiring(false).SetFiringOn([]string{})
	}
	r, err := u.Save(ctx)
	if pdent.IsConstraintError(err) {
		return nil, ErrNameTaken
	}
	return r, err
}

// Delete removes a rule.
func (s *Service) Delete(ctx context.Context, project, id uuid.UUID) error {
	n, err := s.ent.AlertRule.Delete().Where(alertrule.ID(id), alertrule.ProjectID(project)).Exec(ctx)
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// describe is a rule's condition in words, for alerts.
func describe(r *pdent.AlertRule) string {
	scope := ""
	switch {
	case r.Provider != "" && r.Model != "":
		scope = " on " + r.Provider + " for " + r.Model
	case r.Provider != "":
		scope = " on " + r.Provider
	case r.Model != "":
		scope = " for " + r.Model
	}
	window := fmt.Sprintf("%d minutes", r.WindowMinutes)
	if r.WindowMinutes == 60 {
		window = "an hour"
	}
	switch string(r.Kind) {
	case SpendSpike:
		return fmt.Sprintf("spend%s above %g times the usual hour", scope, r.Threshold)
	case ErrorRate:
		return fmt.Sprintf("provider errors%s above %g%% over %s", scope, 100*r.Threshold, window)
	case ProviderDown:
		return fmt.Sprintf("a provider%s failing %g%% of calls over %s", scope, 100*r.Threshold, window)
	}
	return fmt.Sprintf("p95 latency%s above %s over %s", scope, seconds(r.Threshold), window)
}

func seconds(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%.0f ms", ms)
	}
	return fmt.Sprintf("%.1f s", ms/1000)
}
