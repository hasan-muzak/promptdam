// Package schema holds Promptdam's product graph.
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/hasan-muzak/keel/pkg/entx"
)

// Provider is an LLM provider a project calls through the gateway with its
// own key (BYOK): OpenAI, Anthropic, an OpenAI-compatible one (Gemini,
// Mistral, DeepSeek…) or a custom endpoint.
type Provider struct{ ent.Schema }

// Mixin implements ent.Interface.
func (Provider) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (Provider) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(100).Comment("Unique in the project, e.g. openai or openai-backup"),
		field.String("kind").NotEmpty().MaxLen(32).Comment("openai, anthropic, gemini, …, or custom"),
		field.Enum("api_format").Values("openai", "anthropic").Comment("The API the provider speaks"),
		field.String("base_url").NotEmpty().MaxLen(500).Comment("The API root, e.g. https://api.openai.com/v1"),
		field.String("secrets").Default("").Sensitive().Comment("The API key, sealed with pkg/vault"),
		field.String("secret_hint").Default("").MaxLen(100).Comment("The key's last characters (…a1b2)"),
		field.Strings("models").Default([]string{}).
			Comment("Models it serves; empty: the catalog's models of its kind (any model for custom)"),
		field.Int("priority").Default(0).Comment("Lower first, among providers serving the same model"),
		field.Bool("enabled").Default(true),
		field.Time("checked_at").Optional().Nillable().Comment("When the key was last checked against the provider"),
		field.String("check_error").Default("").MaxLen(500).Comment("Why the last check failed; empty when it passed"),
	}
}

// Indexes implements ent.Interface.
func (Provider) Indexes() []ent.Index {
	return []ent.Index{index.Fields("project_id", "name").Unique()}
}
