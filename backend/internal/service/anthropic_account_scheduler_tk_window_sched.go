package service

import (
	"context"
	"time"
)

// anthropicAccountWindowUtilization returns the worse (higher) of 5h/7d upstream
// utilization ratios in [0,1]. ok=false when neither window has a usable signal
// (fail-open for scheduling).
func anthropicAccountWindowUtilization(account *Account, now time.Time) (float64, bool) {
	if account == nil {
		return 0, false
	}
	util, ok := 0.0, false
	if u, k := resolveAnthropicQuotaUtilization(account, "5h", now); k {
		util, ok = u, true
	}
	if u, k := resolveAnthropicQuotaUtilization(account, "7d", now); k && u > util {
		util, ok = u, true
	}
	return util, ok
}

// resolveAnthropicQuotaUtilization returns utilization ratio [0,1] for the given
// window ("5h" or "7d"). ok=false => no usable signal (fail-open).
func resolveAnthropicQuotaUtilization(account *Account, window string, now time.Time) (float64, bool) {
	if account == nil || len(account.Extra) == 0 {
		return 0, false
	}
	switch window {
	case "5h":
		return resolveAnthropic5hUtilization(account, now)
	case "7d":
		return resolveAnthropic7dUtilization(account.Extra, now)
	default:
		return 0, false
	}
}

func resolveAnthropic5hUtilization(account *Account, now time.Time) (float64, bool) {
	if account.SessionWindowEnd != nil && !now.Before(*account.SessionWindowEnd) {
		return 0, false
	}
	utilRaw, ok := account.Extra["session_window_utilization"]
	if !ok {
		return 0, false
	}
	util := parseExtraFloat64(utilRaw)
	if util <= 0 {
		return 0, false
	}
	if account.SessionWindowStart != nil {
		if sampled := parseExtraSampledAt(account.Extra["passive_usage_sampled_at"]); sampled != nil && sampled.Before(*account.SessionWindowStart) {
			return 0, false
		}
	}
	if anthropicPassiveSnapshotStale(account.Extra, now) {
		return 0, false
	}
	return util, true
}

func resolveAnthropic7dUtilization(extra map[string]any, now time.Time) (float64, bool) {
	util := parseExtraFloat64(extra["passive_usage_7d_utilization"])
	if util <= 0 {
		return 0, false
	}
	if resetRaw := parseExtraFloat64(extra["passive_usage_7d_reset"]); resetRaw > 0 {
		resetAt := time.Unix(int64(resetRaw), 0)
		if !now.Before(resetAt) {
			return 0, false
		}
	}
	if anthropicPassiveSnapshotStale(extra, now) {
		return 0, false
	}
	return util, true
}

func anthropicPassiveSnapshotStale(extra map[string]any, now time.Time) bool {
	if len(extra) == 0 {
		return false
	}
	sampled := parseExtraSampledAt(extra["passive_usage_sampled_at"])
	if sampled == nil {
		return false
	}
	return now.Sub(*sampled) >= openAICodexAutoPauseStaleAfter
}

func resolveAnthropicWindowStickyThresholds(_ context.Context, account *Account) (threshold, reserve float64, enabled bool) {
	if account != nil && resolveAccountExtraBool(account.Extra, "anthropic_window_guard_disabled") {
		return 0, 0, false
	}
	return windowUtilStickyThresholdDefault, windowUtilStickyReserveDefault, true
}

// isAccountSchedulableForAnthropicWindow gates Anthropic OAuth/setup-token accounts
// using upstream 5h/7d passive utilization (not local usage_logs dollars).
func (s *GatewayService) isAccountSchedulableForAnthropicWindow(ctx context.Context, account *Account, isSticky bool) bool {
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() {
		return true
	}
	threshold, reserve, enabled := resolveAnthropicWindowStickyThresholds(ctx, account)
	util, ok := anthropicAccountWindowUtilization(account, time.Now())
	return schedulableForWindowUtil(util, ok, threshold, reserve, enabled, isSticky)
}

func leastUtilizedAnthropicAccount(dropped []*Account, now time.Time) *Account {
	return leastUtilizedByWindowUtil(dropped, now, anthropicAccountWindowUtilization)
}
