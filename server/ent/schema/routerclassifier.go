package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/entx"

	"github.com/hasan-muzak/promptdam/server/features/classifier"
)

// RouterClassifier is a model router's classifier, trained by the worker on
// the router's judged calls (router_evals): for each tier but the default,
// a model of which calls it is right for and the calibrated threshold from
// which the gateway places calls on it.
type RouterClassifier struct{ ent.Schema }

// Mixin implements ent.Interface.
func (RouterClassifier) Mixin() []ent.Mixin {
	return []ent.Mixin{entx.BaseMixin{}, entx.ProjectTenantMixin{}}
}

// Fields implements ent.Interface.
func (RouterClassifier) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("router_id", uuid.UUID{}).Comment("The model router"),
		field.JSON("model", classifier.Model{}).Comment("The tiers' models, thresholds and held-out metrics"),
		field.Int("samples").Default(0).Comment("Judged calls it was trained on"),
		field.Time("trained_at"),
	}
}

// Indexes implements ent.Interface.
func (RouterClassifier) Indexes() []ent.Index {
	return []ent.Index{index.Fields("router_id").Unique()}
}
