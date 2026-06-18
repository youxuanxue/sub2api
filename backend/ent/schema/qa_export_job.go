package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
)

// QAExportJob persists the lifecycle of one trajectory-export request so the
// "my exports" panel and the download link survive an app restart/redeploy
// (the prior in-memory job map was wiped on every deploy — see #792 follow-up).
//
// Two kinds:
//   - manual: a user clicked "立即导出" (export now). job_id is a UUID.
//   - auto:   the daily cron archived one (user, api_key, day). job_id is
//     deterministic ("auto:<user>:<api_key>:<YYYY-MM-DD>") so a same-day re-run
//     upserts the same row instead of creating duplicates.
//
// The zip bytes live in the blob store (localfs or, when configured, S3 under
// traj-exports/<user_id>/<api_key_id>/...); this row only tracks status +
// metadata + the storage_key used to build the download URL.
type QAExportJob struct {
	ent.Schema
}

func (QAExportJob) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "qa_export_jobs"},
	}
}

func (QAExportJob) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (QAExportJob) Fields() []ent.Field {
	return []ent.Field{
		// Public job identifier the frontend polls on. UUID for manual jobs;
		// deterministic "auto:<user>:<key>:<date>" for the daily cron (idempotent).
		field.String("job_id").Unique().NotEmpty(),
		field.Int64("user_id"),
		// nil = export spanned all of the user's keys (not currently issued, but
		// keeps the column honest); set for both manual per-key and auto exports.
		field.Int64("api_key_id").Optional().Nillable(),
		// pending | running | done | failed
		field.String("status").Default("pending"),
		// manual | auto
		field.String("export_kind").Default("manual"),
		field.String("format").Default("v2"),
		// Informational coverage window (auto = the archived day; manual = nil).
		field.Time("window_start").Optional().Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("window_end").Optional().Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Blob-store key of the finished zip; empty until status=done.
		field.String("storage_key").Default(""),
		field.Int("record_count").Default(0),
		// Machine error code on failure (no_records / export_failed / busy / interrupted).
		field.String("error").Optional().Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// When the download (and its S3 lifecycle object) expires; nil until done.
		field.Time("expires_at").Optional().Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (QAExportJob) Indexes() []ent.Index {
	return []ent.Index{
		// "my exports" list, newest first.
		index.Fields("user_id", "created_at"),
		// per-key list in the export panel.
		index.Fields("user_id", "api_key_id", "created_at"),
		// housekeeping / TTL filtering.
		index.Fields("expires_at"),
	}
}
