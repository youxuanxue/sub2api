package service

import (
	"context"
	"time"
)

// Window-aware soft scheduling for the OpenAI/GPT line — shares the Codex/Anthropic
// window-util kernel (checkWindowUtilSchedulability + global 0.98/0.02 defaults).
// Signal: account.Extra codex_5h/7d_used_percent (passive upstream snapshot).
//
// Never-empty-pool fallback (leastUtilizedOpenAIAccount) prevents thin pools from
// surfacing empty-pool 429s when every candidate is near its upstream window cap.

func openAIAccountWindowUtilization(account *Account, now time.Time) (float64, bool) {
	if account == nil || len(account.Extra) == 0 {
		return 0, false
	}
	util, ok := 0.0, false
	if u, k := resolveOpenAIQuotaUtilization(account.Extra, "5h", now); k {
		util, ok = u, true
	}
	if u, k := resolveOpenAIQuotaUtilization(account.Extra, "7d", now); k && u > util {
		util, ok = u, true
	}
	return util, ok
}

// CheckOpenAIWindowSchedulability delegates to the shared window-util kernel.
func CheckOpenAIWindowSchedulability(util, stickyThreshold, stickyReserve float64) WindowUtilSchedulability {
	return checkWindowUtilSchedulability(util, stickyThreshold, stickyReserve)
}

func resolveOpenAIWindowStickyThresholds(ctx context.Context, account *Account) (threshold, reserve float64, enabled bool) {
	settings := openAIQuotaAutoPauseSettingsFromContext(ctx)
	if settings.WindowStickyGuardDisabled {
		return 0, 0, false
	}
	if account != nil && resolveAccountExtraBool(account.Extra, "openai_window_guard_disabled") {
		return 0, 0, false
	}

	threshold = windowUtilStickyThresholdDefault
	reserve = windowUtilStickyReserveDefault
	if settings.WindowStickyThreshold > 0 {
		threshold = settings.WindowStickyThreshold
	}
	if settings.WindowStickyReserve > 0 {
		reserve = settings.WindowStickyReserve
	}
	if account != nil {
		if v, ok := resolveAccountExtraNumber(account.Extra, "openai_window_sticky_threshold"); ok && v > 0 {
			threshold = v
		}
		if v, ok := resolveAccountExtraNumber(account.Extra, "openai_window_sticky_reserve"); ok && v >= 0 {
			reserve = v
		}
	}
	return clamp01(threshold), clamp01(reserve), true
}

func (s *OpenAIGatewayService) isAccountSchedulableForOpenAIWindow(ctx context.Context, account *Account, isSticky bool) bool {
	if account == nil {
		return true
	}
	threshold, reserve, enabled := resolveOpenAIWindowStickyThresholds(ctx, account)
	util, ok := openAIAccountWindowUtilization(account, time.Now())
	return schedulableForWindowUtil(util, ok, threshold, reserve, enabled, isSticky)
}

func leastUtilizedOpenAIAccount(dropped []*Account, now time.Time) *Account {
	return leastUtilizedByWindowUtil(dropped, now, openAIAccountWindowUtilization)
}
