package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type QARecord struct {
	ent.Schema
}

func (QARecord) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "qa_records"},
	}
}

func (QARecord) Fields() []ent.Field {
	return []ent.Field{
		field.String("request_id").Unique().NotEmpty(),
		field.Int64("user_id"),
		field.Int64("api_key_id"),
		field.Int64("account_id").Optional().Nillable(),
		field.String("platform").Default("unknown"),
		field.String("requested_model").Default(""),
		field.String("upstream_model").Optional().Nillable(),
		field.String("inbound_endpoint").Default(""),
		field.String("upstream_endpoint").Optional().Nillable(),
		field.Int("status_code").Default(0),
		field.Int64("duration_ms").Default(0),
		field.Int64("first_token_ms").Optional().Nillable(),
		field.Bool("stream").Default(false),
		field.Bool("tool_calls_present").Default(false),
		field.Bool("multimodal_present").Default(false),
		field.Int("input_tokens").Default(0),
		field.Int("output_tokens").Default(0),
		field.Int("cached_tokens").Default(0),
		field.String("request_sha256").Default(""),
		field.String("response_sha256").Default(""),
		field.String("blob_uri").Optional().Nillable(),
		field.JSON("tags", []string{}).Default([]string{}),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("retention_until").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (QARecord) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("created_at"),
		index.Fields("api_key_id", "created_at"),
		index.Fields("user_id", "created_at"),
		index.Fields("platform", "status_code", "created_at"),
	}
}
