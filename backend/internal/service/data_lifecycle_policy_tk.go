package service

import "github.com/Wei-Shaw/sub2api/internal/config"

const (
	prodUsageLogRetentionDays  = 400
	prodErrorLogRetentionDays  = 30
	prodSystemLogRetentionDays = 7
	// Dedup keys only guard against a second claim of the same money event.
	// TokenKey derives every key from a gateway-generated request UUID, so a key
	// is never reused once that event settles. The hot table keeps 7 days; hot
	// plus archive together keep 30, which outlives the slowest settlement:
	// batch-image holds release within batch_image.inflight_ttl_seconds (7 days)
	// and Grok video claims expire after 48 hours. See
	// docs/approved/billing-dedup-retention.md.
	billingDedupRetentionDays        = 7
	billingDedupArchiveRetentionDays = 30
)

type dataLifecyclePolicy struct {
	role                             string
	usageLogRetentionDays            int
	billingDedupRetentionDays        int
	billingDedupArchiveRetentionDays int
	errorLogRetentionDays            int
	systemLogRetentionDays           int
}

func resolveDataLifecyclePolicy(cfg *config.Config, cleanup config.OpsCleanupConfig) dataLifecyclePolicy {
	policy := dataLifecyclePolicy{
		role:                             "prod",
		usageLogRetentionDays:            prodUsageLogRetentionDays,
		billingDedupRetentionDays:        billingDedupRetentionDays,
		billingDedupArchiveRetentionDays: billingDedupArchiveRetentionDays,
		errorLogRetentionDays:            capLifecycleRetention(cleanup.ErrorLogRetentionDays, prodErrorLogRetentionDays),
		systemLogRetentionDays:           capLifecycleRetention(cleanup.SystemLogRetentionDays, prodSystemLogRetentionDays),
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
