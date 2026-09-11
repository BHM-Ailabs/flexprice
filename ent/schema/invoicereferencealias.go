package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"time"
)

type InvoiceReferenceAlias struct{ ent.Schema }

func (InvoiceReferenceAlias) Fields() []ent.Field {
	return []ent.Field{
		field.String("tenant_id").NotEmpty().Immutable(),
		field.String("environment_id").NotEmpty().Immutable(),
		field.String("public_reference").NotEmpty().Immutable(),
		field.String("alias").NotEmpty().Immutable(),
		field.String("normalized_alias").NotEmpty().Immutable(),
		field.String("created_by").Default("").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}
func (InvoiceReferenceAlias) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "environment_id", "normalized_alias").Unique(),
		index.Fields("tenant_id", "environment_id", "public_reference"),
	}
}
