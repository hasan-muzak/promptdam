package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"

	"github.com/hasan-muzak/promptdam/server/features/cachettl"
)

// CachePolicy is what the worker learned of a project's prompt families'
// cache lifetimes (features/cachepolicy): for each family, whether its
// prefix and its conversations' tails are kept an hour, and the gaps that
// weighed it. Small (the families that cache the most, at most 256) and
// rewritten daily.
type CachePolicy struct{ ent.Schema }

// Mixin implements ent.Interface.
func (CachePolicy) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (CachePolicy) Fields() []ent.Field {
	return []ent.Field{
		field.JSON("families", map[string]cachettl.Family{}).Comment("Per family: lifetimes and the gaps that weighed them"),
		field.Time("learned_at"),
	}
}

// Indexes implements ent.Interface.
func (CachePolicy) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id").Unique()}
}
