package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel/pkg/entx"
)

// PriceOverride is a project's own price for a model, over the synced price
// list: a negotiated discount, or a model the list doesn't price (a
// self-hosted one, a host's own model names). USD per million tokens.
type PriceOverride struct{ ent.Schema }

// Mixin implements ent.Interface.
func (PriceOverride) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (PriceOverride) Fields() []ent.Field {
	return []ent.Field{
		field.String("model").NotEmpty().MaxLen(200).Comment("As providers report it; dated snapshots of it match too"),
		field.String("provider").Default("").MaxLen(100).Comment("A provider's name in the project; empty for any"),
		field.Float("input").Min(0),
		field.Float("cached_input").Min(0),
		field.Float("cache_write").Min(0),
		field.Float("output").Min(0),
	}
}

// Indexes implements ent.Interface.
func (PriceOverride) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id", "provider", "model").Unique()}
}
