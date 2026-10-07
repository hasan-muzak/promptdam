package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/entx"
)

// KeySetting is what the gateway applies to one API key (a core api_keys
// row, without an edge across the graphs): the models it may call, its
// developer and tags for attribution, its rate limit and default route. A
// key without a row has the defaults: every model, no limit.
type KeySetting struct{ ent.Schema }

// Mixin implements ent.Interface.
func (KeySetting) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (KeySetting) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("api_key_id", uuid.UUID{}).Comment("The core API key"),
		field.Strings("allowed_models").Default([]string{}).
			Comment("Models the key may call (a trailing * matches a prefix); empty: every model"),
		field.Strings("tags").Default([]string{}).Comment("Tags its requests carry, for analytics"),
		field.String("developer").Default("").MaxLen(200).Comment("Who uses the key, for attribution and budgets"),
		field.Int("rpm").Default(0).Comment("Requests per minute; 0: no limit of its own"),
		field.String("route").Default("").MaxLen(100).Comment("The route its requests take unless they name one"),
	}
}

// Indexes implements ent.Interface.
func (KeySetting) Indexes() []ent.Index {
	return []ent.Index{index.Fields("api_key_id").Unique()}
}
