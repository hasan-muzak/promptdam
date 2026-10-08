package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"
)

// PayloadSegment is one object of a project's kept payloads: many calls'
// payloads, each sealed, written at once. It is deleted, with its object,
// when the plan's retention of its newest payload ends; object storage's
// lifecycle (whole tiers of days) is only a backstop.
type PayloadSegment struct{ ent.Schema }

// Mixin implements ent.Interface.
func (PayloadSegment) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.AppendOnlyMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (PayloadSegment) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").NotEmpty().MaxLen(300).Unique().Comment("The object's key"),
		field.Int64("bytes"),
		field.Int("payloads"),
		field.Time("expires_at"),
	}
}

// Indexes implements ent.Interface.
func (PayloadSegment) Indexes() []ent.Index {
	return []ent.Index{index.Fields("expires_at"), index.Fields("project_id", "expires_at")}
}
