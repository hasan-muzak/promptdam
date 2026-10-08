package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"
)

// PrivacySetting is an organization's privacy: payload-free mode (the
// Business plan's guarantee), while on, keeps any prompt or answer from
// being stored in any of its projects: payload capture, firewall excerpts
// and the response cache are refused, and gateways keep none of them.
type PrivacySetting struct{ ent.Schema }

// Mixin implements ent.Interface.
func (PrivacySetting) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.TenantMixin{}}
}

// Fields implements ent.Interface.
func (PrivacySetting) Fields() []ent.Field {
	return []ent.Field{
		field.Bool("payload_free").Default(false),
		field.String("updated_by").Default("").MaxLen(64).Comment("Who last changed it (usr_…, key_…)"),
	}
}

// Indexes implements ent.Interface.
func (PrivacySetting) Indexes() []ent.Index {
	return []ent.Index{index.Fields("org_id").Unique().StorageKey("privacysetting_org_unique")}
}
