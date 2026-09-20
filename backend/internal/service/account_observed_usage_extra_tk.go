package service

// ObservedUsageWindowExtraKeys is the SSOT list of Extra keys that store
// provider-observed usage / window gauges (UI bars + soft-scheduling signals).
//
// Admin recover-state clears these so operators wait for fresh request evidence
// after an upstream plan upgrade or manual unblock. Local billing counters
// (quota_used / quota_daily_used / …) are intentionally excluded — those belong
// to reset-quota.
//
// model_rate_limits and antigravity_quota_scopes stay on their dedicated clear
// paths (ClearModelRateLimits / ClearAntigravityQuotaScopes) because they are
// scheduling locks, not passive gauges; recover-state already clears them via
// ClearRateLimit.
func ObservedUsageWindowExtraKeys() []string {
	return []string{
		// Anthropic OAuth / Claude Code passive windows
		"session_window_utilization",
		"passive_usage_7d_utilization",
		"passive_usage_7d_reset",
		"passive_usage_7d_sonnet_utilization",
		"passive_usage_7d_sonnet_reset",
		"passive_usage_7d_oi_utilization",
		"passive_usage_7d_oi_reset",
		"passive_usage_sampled_at",

		// NewAPI / VolcEngine Agent Plan / Qianfan / Ali token-plan windows
		NewAPIAccountWindowLockExtraKey,
		newAPIMonthlyUtilExtraKey,
		newAPIMonthlyResetExtraKey,
		newAPIMonthlySampledExtraKey,
		newAPIWeeklyUtilExtraKey,
		newAPIWeeklyResetExtraKey,
		newAPIWeeklySampledExtraKey,
		newAPIFiveHourUtilExtraKey,
		newAPIFiveHourResetExtraKey,
		newAPIFiveHourSampledExtraKey,
		newAPISevenDayUtilExtraKey,
		newAPISevenDayResetExtraKey,
		newAPISevenDaySampledExtraKey,

		// OpenAI OAuth / Codex rolling windows
		"codex_primary_used_percent",
		"codex_primary_reset_after_seconds",
		"codex_primary_window_minutes",
		"codex_secondary_used_percent",
		"codex_secondary_reset_after_seconds",
		"codex_secondary_window_minutes",
		"codex_primary_over_secondary_percent",
		"codex_usage_updated_at",
		"codex_5h_used_percent",
		"codex_5h_reset_after_seconds",
		"codex_5h_window_minutes",
		"codex_5h_reset_at",
		"codex_7d_used_percent",
		"codex_7d_reset_after_seconds",
		"codex_7d_window_minutes",
		"codex_7d_reset_at",

		// Kiro OAuth credits / trial / soft-sched snapshot
		"kiro_usage_current",
		"kiro_usage_limit",
		"kiro_usage_percent",
		"kiro_usage_sampled_at",
		"kiro_next_reset",
		"kiro_subscription_title",
		"kiro_trial_current",
		"kiro_trial_limit",
		"kiro_trial_percent",
		"kiro_trial_status",
		"kiro_trial_expiry",
		"kiro_bonuses",
		"kiro_sched_utilization",
		"kiro_sched_reset_at",

		// Grok / Ollama cloud observed snapshots (not session cookies / toggles)
		"grok_usage_snapshot",
		"grok_billing_snapshot",
		OllamaCloudUsageSnapshotExtraKey,
	}
}
