package service

import "github.com/Wei-Shaw/sub2api/internal/config"

const (
	prodUsageLogRetentionDays  = 90
	prodErrorLogRetentionDays  = 30
	prodSystemLogRetentionDays = 7
	edgeUsageLogRetentionDays  = 7
	edgeErrorLogRetentionDays  = 14
	edgeSystemLogRetentionDays = 3
	billingDedupRetentionDays  = 365
)

type dataLifecyclePolicy struct {
	role                      string
	usageLogRetentionDays     int
	billingDedupRetentionDays int
	errorLogRetentionDays     int
	systemLogRetentionDays    int
}

func resolveDataLifecyclePolicy(cfg *config.Config, cleanup config.OpsCleanupConfig) dataLifecyclePolicy {
	policy := dataLifecyclePolicy{
		role:                      "prod",
		usageLogRetentionDays:     prodUsageLogRetentionDays,
		billingDedupRetentionDays: billingDedupRetentionDays,
		errorLogRetentionDays:     capLifecycleRetention(cleanup.ErrorLogRetentionDays, prodErrorLogRetentionDays),
		systemLogRetentionDays:    capLifecycleRetention(cleanup.SystemLogRetentionDays, prodSystemLogRetentionDays),
	}
	if cfg != nil {
		policy.usageLogRetentionDays = capLifecycleRetention(cfg.DashboardAgg.Retention.UsageLogsDays, prodUsageLogRetentionDays)
		if IsEdgeFrontendURL(cfg.Server.FrontendURL) {
			policy.role = "edge"
			policy.usageLogRetentionDays = capLifecycleRetention(cfg.DashboardAgg.Retention.UsageLogsDays, edgeUsageLogRetentionDays)
			policy.errorLogRetentionDays = capLifecycleRetention(cleanup.ErrorLogRetentionDays, edgeErrorLogRetentionDays)
			policy.systemLogRetentionDays = capLifecycleRetention(cleanup.SystemLogRetentionDays, edgeSystemLogRetentionDays)
		}
	}
	return policy
}

func capLifecycleRetention(configured, upperBound int) int {
	if configured > upperBound {
		return upperBound
	}
	return configured
}

// UsageLogRetentionDays 返回当前节点 usage_logs 的有效保留天数。
// prod 上限 90，edge 上限 7；配置更短时用配置值。展示用，0 或负值回落到 prod 默认。
func UsageLogRetentionDays(cfg *config.Config) int {
	days := resolveDataLifecyclePolicy(cfg, config.OpsCleanupConfig{}).usageLogRetentionDays
	if days <= 0 {
		return prodUsageLogRetentionDays
	}
	return days
}

func dataLifecycleRoleKnown(frontendURL string) bool {
	site := siteFromFrontendURL(frontendURL)
	return site == "prod" || IsEdgeFrontendURL(frontendURL)
}
