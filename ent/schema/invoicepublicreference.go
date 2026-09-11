package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"time"
)

// InvoicePublicReference is an append-only identity registry, separate from financial rows.
type InvoicePublicReference struct{ ent.Schema }

func (InvoicePublicReference) Fields() []ent.Field {
	return []ent.Field{
		field.String("tenant_id").NotEmpty().Immutable(),
		field.String("environment_id").NotEmpty().Immutable(),
		field.String("public_reference").NotEmpty().Immutable(),
		field.String("reservation_key").NotEmpty().Immutable(),
		field.String("invoice_id").Optional().Nillable(),
		field.String("customer_id").Optional().Nillable(),
		field.String("created_by").Default("").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("bound_at").Optional().Nillable(),
	}
}
func (InvoicePublicReference) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "environment_id", "public_reference").Unique(),
		index.Fields("tenant_id", "environment_id", "reservation_key").Unique(),
		index.Fields("tenant_id", "environment_id", "invoice_id").Unique(),
	}
}
