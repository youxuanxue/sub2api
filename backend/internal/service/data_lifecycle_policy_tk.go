package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

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

func dataLifecycleRoleKnown(frontendURL string) bool {
	site := siteFromFrontendURL(frontendURL)
	return site == "prod" || strings.HasPrefix(site, "edge-")
}
