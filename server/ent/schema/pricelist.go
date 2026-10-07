package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel/pkg/entx"
)

// PriceList is a model price list as synced from its source (OpenRouter's
// models API): global reference data, no organization's. Costs are
// computed with the newest; a few older ones are kept to compare against.
type PriceList struct{ ent.Schema }

// Mixin implements ent.Interface.
func (PriceList) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}}
}

// Fields implements ent.Interface.
func (PriceList) Fields() []ent.Field {
	return []ent.Field{
		field.String("source").NotEmpty().MaxLen(500),
		field.Bytes("catalog").Comment("The catalog as pkg/llm reads it (JSON)"),
		field.Int("models").Comment("How many models it prices"),
		field.Int("held").Default(0).Comment("Models whose new price looked wrong and kept the previous one"),
	}
}

// Indexes implements ent.Interface.
func (PriceList) Indexes() []ent.Index {
	return []ent.Index{index.Fields("created_at")}
}
