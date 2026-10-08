package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel-core/pkg/entx"
)

// FirewallSetting is a project's firewall (project.md §6.4): what happens
// to the secrets, personal data and the project's own patterns its calls
// carry, and its CEL policies. A project without a row has the defaults:
// secrets blocked, answers scanned.
type FirewallSetting struct{ ent.Schema }

// Mixin implements ent.Interface.
func (FirewallSetting) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (FirewallSetting) Fields() []ent.Field {
	return []ent.Field{
		// firewall.Settings, raw: the firewall's package reads routing
		// rules' requests, which import this graph.
		field.JSON("settings", json.RawMessage{}).Comment("firewall.Settings"),
	}
}

// Indexes implements ent.Interface.
func (FirewallSetting) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id").Unique()}
}
