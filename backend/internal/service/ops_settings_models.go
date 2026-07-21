package service

// Ops settings models stored in DB `settings` table (JSON blobs).

type OpsEmailNotificationConfig struct {
	Alert  OpsEmailAlertConfig  `json:"alert"`
	Report OpsEmailReportConfig `json:"report"`
	Feishu OpsFeishuAlertConfig `json:"feishu"`
}

type OpsEmailAlertConfig struct {
	Enabled               bool     `json:"enabled"`
	Recipients            []string `json:"recipients"`
	MinSeverity           string   `json:"min_severity"`
	RateLimitPerHour      int      `json:"rate_limit_per_hour"`
	BatchingWindowSeconds int      `json:"batching_window_seconds"`
	IncludeResolvedAlerts bool     `json:"include_resolved_alerts"`
}

type OpsEmailReportConfig struct {
	Enabled                         bool     `json:"enabled"`
	Recipients                      []string `json:"recipients"`
	DailySummaryEnabled             bool     `json:"daily_summary_enabled"`
	DailySummarySchedule            string   `json:"daily_summary_schedule"`
	WeeklySummaryEnabled            bool     `json:"weekly_summary_enabled"`
	WeeklySummarySchedule           string   `json:"weekly_summary_schedule"`
	ErrorDigestEnabled              bool     `json:"error_digest_enabled"`
	ErrorDigestSchedule             string   `json:"error_digest_schedule"`
	ErrorDigestMinCount             int      `json:"error_digest_min_count"`
	AccountHealthEnabled            bool     `json:"account_health_enabled"`
	AccountHealthSchedule           string   `json:"account_health_schedule"`
	AccountHealthErrorRateThreshold float64  `json:"account_health_error_rate_threshold"`
}

type OpsFeishuAlertConfig struct {
	Enabled                 bool   `json:"enabled"`
	WebhookURL              string `json:"webhook_url,omitempty"`
	WebhookURLConfigured    bool   `json:"webhook_url_configured"`
	SigningSecret           string `json:"signing_secret,omitempty"`
	SigningSecretConfigured bool   `json:"signing_secret_configured"`
	RateLimitPerHour        int    `json:"rate_limit_per_hour"`
	CooldownSeconds         int    `json:"cooldown_seconds"`
	// AccountIncidentDigestEnabled 是账号失效事件中「临时冷却」类（429/529/temp）自愈
	// 聚合摘要的总开关。**零值 false = 默认关（opt-in）**——运营判定这类自愈橙头摘要在
	// provider 抖动时是噪音，会淹没真故障 P0。仅当显式设为 true 才发摘要；永久失效 P0、
	// 池级全不可调度 P0、以及 ops 规则 P0 的配对恢复绿卡，走 Feishu 专用路径，
	// 恒发不受此开关影响。
	//
	// 历史上 enable 语义曾错绑在 AccountIncidentDigestSeconds>0（见 PR#730），但
	// normalizeOpsFeishuAlertConfig 的 0→600 回填使 seconds 永不为 0，致 enable 恒真、
	// 默认关从未生效。本字段把 enable 与 interval 彻底解耦：enable 看本 bool，
	// interval 看 seconds。
	AccountIncidentDigestEnabled bool `json:"account_incident_digest_enabled"`
	// AccountIncidentDigestSeconds 控制账号失效事件中「临时冷却」类（429/529/temp）
	// 聚合摘要的 flush 间隔（秒）——**仅间隔，不含 enable 语义**（enable 见上面的
	// AccountIncidentDigestEnabled）。永久失效类即时单发，不受此值影响。默认 600。
	AccountIncidentDigestSeconds int `json:"account_incident_digest_seconds"`
	// PricingMissingDigestSeconds 控制缺价模型零成本流量聚合摘要的 flush 间隔
	// （秒）。首见模型的即时卡不受此值影响。默认 1800。
	PricingMissingDigestSeconds int `json:"pricing_missing_digest_seconds"`
	// UpstreamBalanceLowThresholdCNY 是「上游账号低余额」主动告警的触发阈值（人民币）。
	// 后台 upstream_balance_sentinel 哨兵定时拉有公开余额 API 的上游渠道账号（当前仅
	// DeepSeek channel_type=43）的余额，低于此值时提前发一条橙头飞书预警，让运营在归零
	// 触发全量 402 断供前充值。正向触发级别（非开关）——总开关沿用 Feishu.Enabled。默认 50。
	UpstreamBalanceLowThresholdCNY float64 `json:"upstream_balance_low_threshold_cny"`
}

// OpsEmailNotificationConfigUpdateRequest allows partial updates, while the
// frontend can still send the full config shape.
type OpsEmailNotificationConfigUpdateRequest struct {
	Alert  *OpsEmailAlertConfig  `json:"alert"`
	Report *OpsEmailReportConfig `json:"report"`
	Feishu *OpsFeishuAlertConfig `json:"feishu"`
}

type OpsDistributedLockSettings struct {
	Enabled    bool   `json:"enabled"`
	Key        string `json:"key"`
	TTLSeconds int    `json:"ttl_seconds"`
}

type OpsAlertSilenceEntry struct {
	RuleID     *int64   `json:"rule_id,omitempty"`
	Severities []string `json:"severities,omitempty"`

	UntilRFC3339 string `json:"until_rfc3339"`
	Reason       string `json:"reason"`
}

type OpsAlertSilencingSettings struct {
	Enabled bool `json:"enabled"`

	GlobalUntilRFC3339 string `json:"global_until_rfc3339"`
	GlobalReason       string `json:"global_reason"`

	Entries []OpsAlertSilenceEntry `json:"entries,omitempty"`
}

type OpsMetricThresholds struct {
	SLAPercentMin               *float64 `json:"sla_percent_min,omitempty"`                 // SLA低于此值变红
	TTFTp99MsMax                *float64 `json:"ttft_p99_ms_max,omitempty"`                 // TTFT P99高于此值变红
	RequestErrorRatePercentMax  *float64 `json:"request_error_rate_percent_max,omitempty"`  // 请求错误率高于此值变红
	UpstreamErrorRatePercentMax *float64 `json:"upstream_error_rate_percent_max,omitempty"` // 上游错误率高于此值变红
}

type OpsRuntimeLogConfig struct {
	Level           string         `json:"level"`
	EnableSampling  bool           `json:"enable_sampling"`
	SamplingInitial int            `json:"sampling_initial"`
	SamplingNext    int            `json:"sampling_thereafter"`
	Caller          bool           `json:"caller"`
	StacktraceLevel string         `json:"stacktrace_level"`
	RetentionDays   int            `json:"retention_days"`
	Source          string         `json:"source,omitempty"`
	UpdatedAt       string         `json:"updated_at,omitempty"`
	UpdatedByUserID int64          `json:"updated_by_user_id,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

type OpsAlertRuntimeSettings struct {
	EvaluationIntervalSeconds int `json:"evaluation_interval_seconds"`

	// RateRuleMinSamples is the minimum number of SLA-counted requests that must
	// exist in a rule window before a ratio metric (success_rate / error_rate /
	// upstream_error_rate) is evaluated. Below this floor the metric returns
	// ok=false and the rule is skipped, so a near-empty low-traffic window cannot
	// produce a misleading 100% rate and page a false P0 (2026-06-06 us2/us5: a
	// ~25min window held only 19 / 1 requests, yet a couple of transient upstream
	// blips pushed upstream_error_rate to 100%). 0 (or missing on legacy
	// settings rows) is filled to the default; set 1 to restore the legacy
	// behavior where only the >0 denominator guard applies.
	RateRuleMinSamples int `json:"rate_rule_min_samples"`

	DistributedLock OpsDistributedLockSettings `json:"distributed_lock"`
	Silencing       OpsAlertSilencingSettings  `json:"silencing"`
	Thresholds      OpsMetricThresholds        `json:"thresholds"` // 指标阈值配置
}

// OpsAdvancedSettings stores advanced ops configuration (data retention, aggregation).
type OpsAdvancedSettings struct {
	DataRetention               OpsDataRetentionSettings               `json:"data_retention"`
	Aggregation                 OpsAggregationSettings                 `json:"aggregation"`
	OpenAIAccountQuotaAutoPause OpsOpenAIAccountQuotaAutoPauseSettings `json:"openai_account_quota_auto_pause"`
	IgnoreCountTokensErrors     bool                                   `json:"ignore_count_tokens_errors"`
	IgnoreContextCanceled       bool                                   `json:"ignore_context_canceled"`
	IgnoreNoAvailableAccounts   bool                                   `json:"ignore_no_available_accounts"`
	// Deprecated compatibility field. It is always normalized to true.
	IgnoreInvalidApiKeyErrors       bool `json:"ignore_invalid_api_key_errors"`
	IgnoreInsufficientBalanceErrors bool `json:"ignore_insufficient_balance_errors"`
	DisplayOpenAITokenStats         bool `json:"display_openai_token_stats"`
	DisplayAlertEvents              bool `json:"display_alert_events"`
	AutoRefreshEnabled              bool `json:"auto_refresh_enabled"`
	AutoRefreshIntervalSec          int  `json:"auto_refresh_interval_seconds"`
}

type OpsOpenAIAccountQuotaAutoPauseSettings struct {
	DefaultThreshold5h float64 `json:"default_threshold_5h"`
	DefaultThreshold7d float64 `json:"default_threshold_7d"`
	// TK: window-aware soft scheduling guard (twin of the anthropic window-cost
	// tri-state). Steers new load-balance traffic away from a codex account
	// approaching its 5h/7d window before it 429s, to cut failover hops. The
	// guard is default-ON via built-in thresholds (openAIWindowStickyThreshold
	// Default / ReserveDefault); these fields are operator overrides only. A
	// zero-value struct => guard ON with built-in defaults. WindowStickyGuard
	// Disabled is the global kill-switch (zero-value=false=ON, no redeploy).
	WindowStickyGuardDisabled bool    `json:"window_sticky_guard_disabled"`
	WindowStickyThreshold     float64 `json:"window_sticky_threshold"`
	WindowStickyReserve       float64 `json:"window_sticky_reserve"`
}

type OpsDataRetentionSettings struct {
	CleanupEnabled             bool   `json:"cleanup_enabled"`
	CleanupSchedule            string `json:"cleanup_schedule"`
	ErrorLogRetentionDays      int    `json:"error_log_retention_days"`
	MinuteMetricsRetentionDays int    `json:"minute_metrics_retention_days"`
	HourlyMetricsRetentionDays int    `json:"hourly_metrics_retention_days"`
}

type OpsAggregationSettings struct {
	AggregationEnabled bool `json:"aggregation_enabled"`
}
