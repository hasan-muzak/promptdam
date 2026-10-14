package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"
)

// CacheProfile is how every model uses its prompt cache, learned from all
// projects' calls (features/cacheprofile): global reference data, no
// organization's, written at most hourly and only when it changed. Calls
// are planned with the newest; a few older ones are kept to compare
// against.
type CacheProfile struct{ ent.Schema }

// Mixin implements ent.Interface.
func (CacheProfile) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}}
}

// Fields implements ent.Interface.
func (CacheProfile) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("doc").Comment("The profiles as cacheprofile.Doc reads them (JSON)"),
		field.Int("models").Comment("How many models it profiles"),
	}
}

// Indexes implements ent.Interface.
func (CacheProfile) Indexes() []ent.Index {
	return []ent.Index{index.Fields("created_at")}
}
