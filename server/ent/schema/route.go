package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel/pkg/entx"
)

// Route is a named path requests take through the gateway: what to try when
// the model asked for fails, and whether identical requests are answered
// from the response cache. Requests pick one with X-Promptdam-Route, else
// their key's, else the project's route named "default".
type Route struct{ ent.Schema }

// Mixin implements ent.Interface.
func (Route) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (Route) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(100),
		field.Strings("fallbacks").Default([]string{}).
			Comment("Tried in order when the model asked for fails: model, or provider/model"),
		field.Int("retries").Default(1).Comment("Retries of a target, with jitter, before the next one"),
		field.Int("cache_ttl_seconds").Default(0).
			Comment("Answer identical non-streamed requests from the response cache for this long; 0: off"),
	}
}

// Indexes implements ent.Interface.
func (Route) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id", "name").Unique()}
}
