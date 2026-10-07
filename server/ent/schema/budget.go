package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel/pkg/entx"
)

// Budget caps what a scope of a project spends on LLM calls per day or
// month (UTC): the whole project, one API key, one developer or one route.
// Crossing a threshold alerts once per period; a blocking budget also
// refuses the scope's calls once it is spent.
type Budget struct{ ent.Schema }

// Mixin implements ent.Interface.
func (Budget) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (Budget) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(100),
		field.Enum("scope").Values("project", "key", "developer", "route"),
		field.String("target").Default("").MaxLen(200).
			Comment("The key's ID, the developer or the route's name; empty for the project"),
		field.Enum("window").Values("day", "month"),
		field.Int64("limit_micros").Positive().Comment("Millionths of a dollar per window"),
		field.Enum("action").Values("alert", "block").Default("alert"),
		field.Ints("thresholds").Default([]int{80, 100}).Comment("Percents of the limit that alert, ascending"),
		field.Strings("channel_ids").Default([]string{}).Comment("Core notification channels; empty for all of the organization's"),
		field.Bool("enabled").Default(true),
		field.Time("alerted_period").Optional().Nillable().Comment("Start of the window the last alert was in"),
		field.Int("alerted_percent").Default(0).Comment("Highest threshold alerted in that window"),
	}
}

// Indexes implements ent.Interface.
func (Budget) Indexes() []ent.Index {
	return []ent.Index{index.Fields("enabled", "project_id")}
}
