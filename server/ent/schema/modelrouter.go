package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"

	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

// ModelRouter places each call it gets in one of its tiers of models, from
// cheapest to strongest (project.md §6.2): the client's explicit tier, its
// rules, the classifier learned from judged samples of the project's calls,
// the decision model when the classifier is unsure, else its default tier.
// Clients call it like a model ("model": "<name>"), or a route sends its
// calls through it.
type ModelRouter struct{ ent.Schema }

// Mixin implements ent.Interface.
func (ModelRouter) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (ModelRouter) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(64).Comment("Clients call it as a model: name, or name:tier"),
		field.Enum("status").Values(tiers.Shadow, tiers.Active, tiers.Disabled).Default(tiers.Shadow).
			Comment("shadow decides and evaluates but serves the default tier"),
		field.JSON("tiers", []tiers.Tier{}).Comment("From cheapest to strongest"),
		field.JSON("rules", []tiers.Rule{}).Default([]tiers.Rule{}).Comment("CEL conditions over request, each to a tier; the first that holds"),
		field.String("default_tier").NotEmpty().MaxLen(32).
			Comment("Calls nothing places get it; the others' answers are judged against it"),
		field.Float("quality_floor").Default(0.95).
			Comment("The share of a tier's answers that must hold up against the default tier's"),
		field.Float("sample_rate").Default(0).Comment("The share of calls evaluated; 0: 10% in shadow mode, 2% active"),
		field.Int64("eval_budget_micros").Default(100_000_000).
			Comment("What evaluation (replays, judge, decision model) may spend a month, on the project's keys"),
		field.String("judge").Default("").MaxLen(300).
			Comment("The model that judges answers: model or provider/model; empty: claude-sonnet-5-5 when an Anthropic provider serves it"),
		field.String("decision_model").Default("").MaxLen(300).
			Comment("Asked when the classifier is unsure: model or provider/model; empty: never"),
		field.Int("decision_timeout_ms").Default(150).Comment("How long the decision model may take before the default tier serves"),
		field.Time("activated_at").Optional().Nillable(),
		field.JSON("demoted", []tiers.Demotion{}).Default([]tiers.Demotion{}).Annotations(entsql.Default("[]")).
			Comment("Tiers the canary demoted: the router places no call on them until restored"),
		field.Time("ready_at").Optional().Nillable().
			Comment("When the router's evidence first met its floor (router.ready); cleared when it changes"),
	}
}

// Indexes implements ent.Interface.
func (ModelRouter) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id", "name").Unique()}
}
