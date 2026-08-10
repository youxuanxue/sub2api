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

// QAArchiveShard tracks hourly raw-archive control state (design-prod-qa-24h §14.1).
type QAArchiveShard struct {
	ent.Schema
}

func (QAArchiveShard) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "qa_archive_shards",
			Checks: map[string]string{
				"qa_archive_shards_forward_cutover_valid": "NOT forward_cutover OR (state = 'committed' AND restore_verified_at IS NOT NULL)",
			},
		},
	}
}

func (QAArchiveShard) Fields() []ent.Field {
	return []ent.Field{
		field.Time("window_start").
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("window_end").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int("generation").Default(0),
		field.String("state").Default("pending"),
		field.Int64("record_count").Default(0),
		field.Int64("blob_ref_count").Default(0),
		field.Int64("blob_present_count").Default(0),
		field.Int64("blob_missing_count").Default(0),
		field.Int64("logical_bytes").Default(0),
		field.Int64("artifact_bytes").Default(0),
		field.JSON("checksums", map[string]string{}).Default(map[string]string{}),
		field.String("s3_prefix").Default(""),
		field.String("manifest_key").Optional().Nillable(),
		field.String("commit_key").Optional().Nillable(),
		field.String("commit_etag").Optional().Nillable(),
		field.Int64("aggregate_record_count").Default(0),
		field.Int64("aggregate_blob_ref_count").Default(0),
		field.Int64("aggregate_blob_present_count").Default(0),
		field.Int64("aggregate_blob_missing_count").Default(0),
		field.Time("verified_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("restore_verified_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("verification_error_code").Optional().Nillable(),
		field.Time("last_reconciled_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("final_reconciled_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Bool("cleanup_eligible").Default(false),
		field.Bool("forward_cutover").Default(false),
		field.Time("first_attempt_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("completed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("last_error").Optional().Nillable(),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (QAArchiveShard) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("window_start", "generation").Unique(),
		index.Fields("state", "window_start"),
		index.Fields("forward_cutover").
			StorageKey("idx_qa_archive_shards_one_forward_cutover").
			Unique().
			Annotations(entsql.IndexWhere("forward_cutover")),
	}
}
