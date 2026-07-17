package service

import (
	"context"
	"strings"
	"time"
)

const grokProbeRetryTTL = time.Minute

func (s *AccountUsageService) getGrokUsage(ctx context.Context, account *Account, force bool) (*UsageInfo, error) {
	if s.grokQuotaFetcher == nil {
		return s.buildLocalWindowUsage(ctx, account), nil
	}
	var billingProbeResult *GrokQuotaProbeResult
	if account != nil && account.IsGrokOAuth() && s.grokQuotaService != nil &&
		(force || grokBillingSnapshotNeedsRefresh(account, time.Now())) &&
		s.shouldProbeGrokBilling(account.ID, time.Now(), force) {
		result, err := s.grokQuotaService.ProbeBilling(ctx, account.ID)
		if err == nil && result != nil && result.Billing != nil {
			billingProbeResult = result
			mergeAccountExtra(account, map[string]any{grokBillingExtraKey: result.Billing})
		} else if err != nil && force {
			return nil, err
		}
	}
	usage := s.grokQuotaFetcher.BuildUsageInfo(account)
	if usage.GrokQuotaSnapshotState == "" {
		if usage.ErrorCode == "quota_unknown" {
			usage.GrokQuotaSnapshotState = "unknown_until_first_response"
		} else {
			usage.GrokQuotaSnapshotState = "observed"
		}
	}
	if account != nil {
		if s.usageLogRepo != nil {
			if stats, err := s.usageLogRepo.GetAccountTodayStats(ctx, account.ID); err == nil && stats != nil {
				usage.GrokLocalUsage = windowStatsFromAccountStats(stats)
			}
		}
		if billingProbeResult != nil {
			usage.GrokLocalUsage24h = billingProbeResult.LocalUsage24h
			usage.GrokLocalUsage7d = billingProbeResult.LocalUsage7d
			usage.GrokLocalUsageMonthly = billingProbeResult.LocalUsageMonthly
		} else if s.usageLogRepo != nil {
			usage.GrokLocalUsage24h, usage.GrokLocalUsage7d, usage.GrokLocalUsageMonthly = grokLocalUsageForQuota(
				ctx, s.usageLogRepo, account.ID, usage.GrokBilling, time.Now().UTC(),
			)
		}
	}
	enrichUsageWithAccountError(usage, account)
	return usage, nil
}

func grokBillingSnapshotNeedsRefresh(account *Account, now time.Time) bool {
	if account == nil {
		return false
	}
	billing, err := grokBillingSnapshotFromExtra(account.Extra)
	if err != nil || billing == nil || billing.Partial || len(billing.FailedWindows) > 0 {
		return true
	}
	stamp := strings.TrimSpace(billing.UpdatedAt)
	if stamp == "" {
		stamp = strings.TrimSpace(billing.FetchedAt)
	}
	updatedAt, err := parseTime(stamp)
	return err != nil || now.Sub(updatedAt) >= openAIProbeCacheTTL
}

func (s *AccountUsageService) shouldProbeGrokBilling(accountID int64, now time.Time, force bool) bool {
	if force || s == nil || s.cache == nil || accountID <= 0 {
		return true
	}
	if cached, ok := s.cache.grokProbeCache.Load(accountID); ok {
		if timestamp, ok := cached.(time.Time); ok && now.Sub(timestamp) < grokProbeRetryTTL {
			return false
		}
	}
	s.cache.grokProbeCache.Store(accountID, now)
	return true
}
