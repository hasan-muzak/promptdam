package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"
)

// FamilyLabel is the name a project gave one of its prompt families (its
// task: "invoice extraction"). Families themselves are found in the request
// log, not stored.
type FamilyLabel struct{ ent.Schema }

// Mixin implements ent.Interface.
func (FamilyLabel) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (FamilyLabel) Fields() []ent.Field {
	return []ent.Field{
		field.String("family_id").NotEmpty().MaxLen(110),
		field.String("label").NotEmpty().MaxLen(100),
	}
}

// Indexes implements ent.Interface.
func (FamilyLabel) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id", "family_id").Unique()}
}
