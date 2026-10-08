package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"
)

// CaptureSetting is whether a project keeps its calls' payloads: the
// request as the provider got it and the answer the client got, secrets
// and personal data always masked, kept as long as the plan keeps logs. A
// project without a row keeps none.
type CaptureSetting struct{ ent.Schema }

// Mixin implements ent.Interface.
func (CaptureSetting) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (CaptureSetting) Fields() []ent.Field {
	return []ent.Field{
		field.Bool("enabled").Default(false),
		field.Float("sample_rate").Default(1).Comment("The share of calls kept, 0 to 1"),
		field.Bool("answers").Default(true).Comment("Keep answers too, not only requests"),
	}
}

// Indexes implements ent.Interface.
func (CaptureSetting) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id").Unique()}
}
