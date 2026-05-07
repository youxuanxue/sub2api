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
// Population sources (see docs/approved/pricing-availability-source-of-truth.md):
//   - Passive: every successful gateway forward writes (platform, upstream_model,
//     status, account_id) here via a 1-line hook in
//     gateway_service.go recordUsageCore. 真实 OPC 流量是免费样本来源。
//   - Passive (failure): handlers (gateway_handler_chat_completions /
//     gateway_handler_responses / gemini_v1beta_handler) call RecordOutcome on
//     forward errors, classifying the failure (model_not_found vs rate_limited
//     vs upstream_5xx etc.) so we don't conflate "Google rate-limited us" with
//     "model is unreachable".
//   - Active backstop: pricing_availability_seeder_tk.go enables a
//     channel_monitors row with kind=system_availability for catalog cells that
//     have been silent >24h. Reuses ChannelMonitorRunner; no new scheduler.
//
// Status semantics:
//   - ok           — verified within 24h AND 24h success rate >=95%
//   - stale        — 24h success rate 80-95%, OR last_seen_ok >24h ago
//   - unreachable  — last_failure_kind=model_not_found (single sample) OR
//                    24h success rate <80%
//   - untested     — no samples ever
//
// last_failure_kind taxonomy (kept narrow, do not invent):
//   - "" (cleared on success)
//   - model_not_found  / not_found  — strong / medium signal, model-level
//   - rate_limited / auth_failure   — INCONCLUSIVE, account-level; do not flip
//                                     status, only refresh last_checked_at
//   - upstream_5xx / network_error / bad_response_shape — soft signal, accumulate
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
		field.Enum("platform").
			Values("openai", "anthropic", "gemini", "antigravity", "newapi"),
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
		// seeder 选 cold-tail：先按 status 筛 untested/stale 再按 last_checked_at 排序
		index.Fields("status", "last_checked_at"),
	}
}
