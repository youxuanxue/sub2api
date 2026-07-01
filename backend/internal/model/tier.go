// Package model 定义服务层使用的数据模型。
package model

import "time"

// Tier 是 anthropic OAuth 稳定性档位（l1..l5）的领域模型，和
// TLSFingerprintProfile 同构：账号通过 tier_id 引用，运行时按 id 解析 per-tier
// 配置。本表是 git baseline JSON 的投影。
type Tier struct {
	ID                        int64     `json:"id"`
	Name                      string    `json:"name"`
	Description               *string   `json:"description"`
	Concurrency               int       `json:"concurrency"`
	Priority                  int       `json:"priority"`
	RateMultiplier            float64   `json:"rate_multiplier"`
	BaseRPM                   int       `json:"base_rpm"`
	MaxSessions               int       `json:"max_sessions"`
	RPMStickyBuffer           int       `json:"rpm_sticky_buffer"`
	SessionIdleTimeoutMinutes int       `json:"session_idle_timeout_minutes"`
	CacheTTLOverrideEnabled   bool      `json:"cache_ttl_override_enabled"`
	CacheTTLOverrideTarget    *string   `json:"cache_ttl_override_target"`
	TLSProfileName            *string   `json:"tls_profile_name"`
	TLSProfileID              *int64    `json:"tls_profile_id"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

// Validate 校验档位配置基本有效性。
func (t *Tier) Validate() error {
	if t.Name == "" {
		return &ValidationError{Field: "name", Message: "name is required"}
	}
	return nil
}

// TierManagedExtraKeys 是 tier 在运行时 overlay 进 account.Extra 的「策略类」键。
// 这些键由 tier 表解析、内存覆盖（读路径），绝不持久化到账号（写路径在 repo.Update
// 经 TierManagedExtraStripped 剥离），从而满足「改 tier 全生效 + 零账号写」。
var TierManagedExtraKeys = []string{
	"base_rpm",
	"max_sessions",
	"rpm_sticky_buffer",
	"session_idle_timeout_minutes",
	"cache_ttl_override_enabled",
	"cache_ttl_override_target",
}

// IsTierManagedExtraKey 报告 key 是否由 tier overlay 管理（写路径据此剥离）。
func IsTierManagedExtraKey(key string) bool {
	for _, k := range TierManagedExtraKeys {
		if k == key {
			return true
		}
	}
	return false
}

// OverlayExtra 把本档位的「策略类」per-tier 数值字段覆盖进给定的（内存）extra
// map（账号加载边界调用，不持久化）。仅覆盖与运行时 getter 对应的字段；TLS 绑定
// 与 concurrency 不在此处（前者 apply 时持久化、后者由 reconciler 值同步到列）。
func (t *Tier) OverlayExtra(extra map[string]any) {
	if extra == nil {
		return
	}
	extra["base_rpm"] = t.BaseRPM
	extra["max_sessions"] = t.MaxSessions
	extra["rpm_sticky_buffer"] = t.RPMStickyBuffer
	extra["session_idle_timeout_minutes"] = t.SessionIdleTimeoutMinutes
	extra["cache_ttl_override_enabled"] = t.CacheTTLOverrideEnabled
	if t.CacheTTLOverrideTarget != nil {
		extra["cache_ttl_override_target"] = *t.CacheTTLOverrideTarget
	}
}
