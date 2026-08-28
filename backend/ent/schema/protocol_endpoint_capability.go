package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ProtocolEndpointCapability owns the native text protocols supported by one
// canonical upstream endpoint identity. Credentials remain on Account rows;
// every governed account links to exactly one shared capability row.
type ProtocolEndpointCapability struct {
	ent.Schema
}

func (ProtocolEndpointCapability) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "protocol_endpoint_capabilities"}}
}

func (ProtocolEndpointCapability) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (ProtocolEndpointCapability) Fields() []ent.Field {
	return []ent.Field{
		field.String("capability_key").NotEmpty().MaxLen(64).Unique().Immutable(),
		field.JSON("identity", map[string]any{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Immutable(),
		field.JSON("supported_protocols", []string{}).
			Default(func() []string { return []string{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("probe_evidence", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int64("revision").Default(1).Positive(),
		field.Time("last_probed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("probe_lease_owner").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("probe_lease_until").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int64("probe_generation").Default(0).NonNegative(),
		field.Bool("identity_conflict").Default(false),
	}
}

func (ProtocolEndpointCapability) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("accounts", Account.Type).
			Annotations(entsql.OnDelete(entsql.Restrict)),
	}
}

func (ProtocolEndpointCapability) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("probe_lease_until"),
		index.Fields("identity_conflict"),
	}
}
