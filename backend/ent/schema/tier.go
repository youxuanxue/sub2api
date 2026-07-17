// Package schema 定义 Ent ORM 的数据库 schema。
package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// Tier 定义 Anthropic OAuth 稳定性档位（l1..l5）的 schema。
//
// Tier 是一个「命名引用实体」，和 TLSFingerprintProfile 同构：anthropic OAuth
// 账号通过 Account.tier_id 绑定到某个 tier，运行时按 id 解析该档位的 per-tier
// 配置（base_rpm / max_sessions / rpm_sticky_buffer / concurrency /
// TLS 绑定等），而不再把这些值逐个拷贝到账号上。改一行 tier 经
// Redis pub/sub 秒级 fan-out 到所有引用账号（账号零写）。
//
// 权威源是 git JSON（backend/internal/baseline + deploy/aws/stage0，sentinel
// 锁定相等）；本表是 git 的「投影」，由 ops/anthropic Python 流水线同步进各节点
// （prod + edge）。UI 可编辑（应急/本地），流水线每次运行会重断言。
type Tier struct {
	ent.Schema
}

// Annotations 返回 schema 的注解配置。
func (Tier) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "tiers"},
	}
}

// Mixin 返回该 schema 使用的混入组件。
// 仅 TimeMixin（与 TLSFingerprintProfile 同构）：tier 是参考数据，硬删除，
// name 走普通唯一约束。
func (Tier) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields 定义 tier 实体的所有字段。每个 per-tier 字段对应 baseline JSON 里
// tiers[name].baseline.account.* / .extra.* 的一项（标量列，便于 SQL 投影 +
// UI 编辑）。
func (Tier) Fields() []ent.Field {
	return []ent.Field{
		// name: 档位名，唯一标识，如 "l1".."l5"
		field.String("name").
			MaxLen(100).
			NotEmpty().
			Unique(),

		// description: 档位描述
		field.Text("description").
			Optional().
			Nillable(),

		// ===== 调度类（concurrency 是 oauth 账号 concurrency 的写入源；priority 仅投影，不下发） =====
		field.Int("concurrency").
			Default(3).
			Comment("oauth account concurrency write-source (value-synced onto account.concurrency)"),
		field.Int("priority").
			Default(50).
			Comment("projection only — accounts.priority is owned by the window-rebalance pipeline, NOT pushed from here"),
		field.Float("rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1.0),

		// ===== 策略类（运行时按 tier 解析，账号零写；0/默认 = 未启用） =====
		field.Int("base_rpm").Default(0),
		field.Int("max_sessions").Default(0),
		field.Int("rpm_sticky_buffer").Default(0),
		field.Int("session_idle_timeout_minutes").Default(8),
		field.Bool("cache_ttl_override_enabled").Default(false),
		field.String("cache_ttl_override_target").
			MaxLen(20).
			Optional().
			Nillable(),

		// ===== TLS 指纹绑定（tier → TLSFingerprintProfile） =====
		// tls_profile_name: 流水线按名 upsert TLS profile 后回填 id。
		field.String("tls_profile_name").
			MaxLen(100).
			Optional().
			Nillable(),
		field.Int64("tls_profile_id").
			Optional().
			Nillable().
			Comment("bound TLS fingerprint profile id; resolved at runtime via ResolveTLSProfile"),
	}
}
