package service

import "github.com/Wei-Shaw/sub2api/internal/config"

const (
	prodUsageLogRetentionDays  = 90
	prodErrorLogRetentionDays  = 30
	prodSystemLogRetentionDays = 7
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

	}
	return policy
}

func capLifecycleRetention(configured, upperBound int) int {
	if configured > upperBound {
		return upperBound
	}
	return configured
}

func UsageLogRetentionDays(cfg *config.Config) int {
	days := resolveDataLifecyclePolicy(cfg, config.OpsCleanupConfig{}).usageLogRetentionDays
	if days <= 0 {
		return prodUsageLogRetentionDays
	}
	return days
}

func dataLifecycleRoleKnown(frontendURL string) bool {
	site := siteFromFrontendURL(frontendURL)
	return site == "prod" || site == "cn-prod"
}
