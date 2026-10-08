package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"
)

// AlertRule watches a project's calls and alerts when they go wrong: spend
// that spikes above its usual hour, provider errors, a provider that looks
// down, slow answers. The worker evaluates enabled rules every minute; a
// rule fires once on crossing, again after its cooldown while still firing,
// and resolves when the condition clears.
type AlertRule struct{ ent.Schema }

// Mixin implements ent.Interface.
func (AlertRule) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (AlertRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(100),
		field.Enum("kind").Values("spend_spike", "error_rate", "provider_down", "latency"),
		field.Float("threshold").Comment("spend_spike: times the usual hour; error_rate, provider_down: share of calls; latency: p95 in ms"),
		field.Int("window_minutes").Comment("5, 15, 30 or 60 (spend_spike: 60)"),
		field.Int("min_calls").Default(0).Comment("Fewer calls in the window can't fire it"),
		field.Int64("min_micros").Default(0).Comment("spend_spike: the least the hour must cost to fire"),
		field.String("provider").Default("").MaxLen(100).Comment("Only this provider's calls; empty for all (provider_down: each)"),
		field.String("model").Default("").MaxLen(200).Comment("Only calls to this model; empty for all"),
		field.Strings("channel_ids").Default([]string{}).Comment("Alert channels; empty for all of the organization's"),
		field.Bool("enabled").Default(true),
		field.Int("cooldown_minutes").Default(60).Comment("While firing, it alerts again after this long"),
		field.Bool("firing").Default(false),
		field.Strings("firing_on").Default([]string{}).Comment("provider_down: the providers that look down"),
		field.Float("last_value").Default(0).Comment("What the last evaluation measured, in the threshold's unit"),
		field.Time("evaluated_at").Optional().Nillable(),
		field.Time("fired_at").Optional().Nillable(),
		field.Time("resolved_at").Optional().Nillable(),
	}
}

// Indexes implements ent.Interface.
func (AlertRule) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id", "name").Unique(), index.Fields("enabled")}
}
