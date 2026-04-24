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
		// Synthetic-pipeline tagging (issue #59 / docs/projects/auto-traj-from-supply-demand.md §6.1).
		// All four fields are nullable / have defaults so existing online callers are NOT affected.
		// Populated by qa.Middleware from request headers X-Synth-Session, X-Synth-Role,
		// X-Synth-Engineer-Level, X-Synth-Pipeline; absent for normal traffic (non-synth).
		field.String("synth_session_id").Optional().Nillable(),
		field.String("synth_role").Optional().Nillable(),
		field.String("synth_engineer_level").Optional().Nillable(),
		field.Bool("dialog_synth").Default(false),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("retention_until").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (QARecord) Indexes() []ent.Index {
	// Issue #59 lookup `WHERE user_id = ? AND synth_session_id = ?` is
	// served by the partial index in tk_006_add_qa_records_synth_fields.sql
	// (`WHERE synth_session_id IS NOT NULL`). We deliberately do NOT add
	// a non-partial copy here so production runs only one synth-session
	// index (Ent's portable Indexes() API can't express the WHERE clause
	// and the SQL migration is this repo's source of truth for schema).
	return []ent.Index{
		index.Fields("created_at"),
		index.Fields("api_key_id", "created_at"),
		index.Fields("user_id", "created_at"),
		index.Fields("platform", "status_code", "created_at"),
	}
}
