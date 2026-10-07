package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel/pkg/entx"
)

// RoutingRule sends the calls matching a condition (CEL, over the request
// as the gateway sees it) to another model. Active rules are evaluated in
// priority order on a route's calls (or every route's); the first match
// routes the call, the model asked for becoming its first fallback.
type RoutingRule struct{ ent.Schema }

// Mixin implements ent.Interface.
func (RoutingRule) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (RoutingRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(100),
		field.String("route").Default("").MaxLen(100).Comment("The route whose calls it reads; empty for every route"),
		field.String("when").NotEmpty().MaxLen(4096).Comment("CEL condition over request"),
		field.String("to_model").NotEmpty().MaxLen(300).Comment("The model to send matching calls to: model or provider/model"),
		field.Int("priority").Default(0).Comment("Lower first"),
		field.Enum("status").Values("draft", "active", "disabled").Default("draft"),
		field.Time("activated_at").Optional().Nillable(),
	}
}

// Indexes implements ent.Interface.
func (RoutingRule) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id", "name").Unique(), index.Fields("project_id", "status")}
}
