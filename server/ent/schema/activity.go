package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"
)

// Activity is something that happened to a project and was worth telling:
// a budget threshold crossed, a router ready or a tier demoted, calls the
// firewall refused, an alert rule firing. The organization's channels and
// webhooks got it as it happened; the dashboard reads it here. Kept 90 days.
type Activity struct{ ent.Schema }

// Mixin implements ent.Interface.
func (Activity) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.AppendOnlyMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (Activity) Fields() []ent.Field {
	return []ent.Field{
		field.String("kind").NotEmpty().MaxLen(64).Comment("As its webhook event names it: budget.exceeded, router.ready…"),
		field.Enum("severity").Values("info", "warning", "critical").Default("info"),
		field.String("title").NotEmpty().MaxLen(300),
		field.String("body").Default("").MaxLen(4000),
		field.String("subject").Default("").MaxLen(64).Comment("What it is about: a budget (bdg_…), a router (rtr_…), an alert rule (alr_…)"),
		field.String("url").Default("").MaxLen(500).Comment("The dashboard's page for it"),
		field.JSON("data", map[string]any{}).Default(map[string]any{}).Comment("Its numbers, as its webhook carries them"),
	}
}

// Indexes implements ent.Interface.
func (Activity) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id", "id"), index.Fields("project_id", "subject", "id"),
		index.Fields("created_at")} // pruning
}
