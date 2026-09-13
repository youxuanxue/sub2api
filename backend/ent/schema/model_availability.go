package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ModelAvailability is the per-(platform, model) verified-availability state
// that backs the public catalog at /api/v1/public/pricing.
//
// Protocol execution and the legacy billing fallback share the observation
// writer in PricingAvailabilityService; this table owns neither scheduling nor
// serving policy. See docs/approved/pricing-availability-source-of-truth.md.
//
// Status and failure-kind semantics are owned by PricingAvailabilityService and
// docs/approved/pricing-availability-source-of-truth.md. This table stores
// observations; a request-level model_not_found is not provider-wide retirement.
type ModelAvailability struct {
	ent.Schema
}

func (ModelAvailability) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "model_availability"},
	}
}

func (ModelAvailability) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (ModelAvailability) Fields() []ent.Field {
	return []ent.Field{
		// SSOT: must match domain.Platform* constants in
		// internal/domain/constants.go (all 7 gateway platforms).
		field.Enum("platform").
			Values("openai", "anthropic", "gemini", "antigravity", "newapi", "kiro", "grok"),
		field.String("model_id").
			NotEmpty().
			MaxLen(200),
		field.Enum("status").
			Values("ok", "stale", "unreachable", "untested").
			Default("untested"),
		field.Time("last_seen_ok_at").
			Optional().
			Nillable(),
		field.Time("last_failure_at").
			Optional().
			Nillable(),
		field.String("last_failure_kind").
			Default("").
			MaxLen(50),
		field.Int("upstream_status_code_last").
			Optional().
			Nillable(),
		field.Time("last_checked_at").
			Optional().
			Nillable(),
		field.Int("sample_ok_24h").
			Default(0),
		field.Int("sample_total_24h").
			Default(0),
		field.Time("rolling_window_started_at").
			Optional().
			Nillable(),
		// last_account_id: 信息字段，无 FK 约束（账号可能被删，留 stale id 无害）
		field.Int64("last_account_id").
			Optional().
			Nillable(),
	}
}

func (ModelAvailability) Indexes() []ent.Index {
	return []ent.Index{
		// 主查询：catalog handler 按 (platform, model_id) 取最新 availability
		index.Fields("platform", "model_id").Unique(),
		// Support status/time evidence queries.
		index.Fields("status", "last_checked_at"),
	}
}
