package service

import (
	"strings"
	"time"
)

// MiniMax Coding/Token Plan (api.minimaxi.com / api.minimax.io) returns a Chinese
// 429 without reset text when the rolling 5h (or weekly) included quota is spent:
//
//	已达到 Token Plan 用量上限：请升级 Token Plan 套餐或购买积分补充用量。 (2056)
//
// Prod 2026-11-20 (accounts 578/591/592/593): that copy was confirmed against
// coding_plan/remains as current_interval remaining=0 while weekly still had
// headroom. The generic NewAPI usage-window parser only recognized English
// exhausted/exceeded + reset-at phrasing, so handle429 fell through to the
// 5-second 429 fallback and the pool hammered the same keys until ready=0.
//
// This file owns the MiniMax Token Plan wording gate, NewAPI+minimaxi host
// recognition (Extension Engine accounts are platform=newapi, not platform=
// minimax), and the no-reset fallback (5h window). Remains snapshots land in
// Extra newapi_5h_* / newapi_weekly_* (and minimax_* via CNProviderQuotaService)
// so the existing admin usage-window bars can render remaining %.

const (
	// tkMiniMaxTokenPlanQuotaCode is the upstream parenthetical code observed on
	// api.minimaxi.com Token Plan 429 bodies.
	tkMiniMaxTokenPlanQuotaCode = "2056"
	// tkMiniMaxTokenPlanQuotaFallback is the scheduling cool when the body has no
	// reset timestamp and Extra has no 5h/weekly snapshot yet. Matches the
	// official current_interval (5h) window size — prefer remains/Extra when known.
	tkMiniMaxTokenPlanQuotaFallback = 5 * time.Hour
	// newAPIUsageWindowObservedExtraKey marks a proactive remains/usage snapshot
	// so applyNewAPIUsageWindowSnapshot can render partial bars without treating
	// the row as a Volc-style false window lock (which requires window_lock).
	newAPIUsageWindowObservedExtraKey = "newapi_usage_window_observed"
)

// isNewAPIMiniMaxTokenPlanAccount reports NewAPI Extension Engine accounts that
// forward to official MiniMax Token/Coding Plan hosts. They share remains with
// native platform=minimax coding accounts but are stored as platform=newapi
// without credentials.account_mode=coding.
func isNewAPIMiniMaxTokenPlanAccount(account *Account) bool {
	if account == nil || account.Platform != PlatformNewAPI {
		return false
	}
	base := strings.ToLower(strings.TrimSpace(account.GetOpenAIBaseURL()))
	if base == "" {
		base = strings.ToLower(strings.TrimSpace(account.GetCredential("base_url")))
	}
	return strings.Contains(base, "minimaxi.com") ||
		strings.Contains(base, "minimax.io") ||
		strings.Contains(base, "minimax.com")
}

// mirrorMiniMaxRemainsToNewAPIUsageWindowExtra maps CN remains tiers (used %)
// onto the NewAPI usage-window Extra keys consumed by OpenAIUsageCell /
// applyNewAPIUsageWindowSnapshot and by 429 cool preference.
func mirrorMiniMaxRemainsToNewAPIUsageWindowExtra(tiers []CNQuotaTier, now time.Time) map[string]any {
	if len(tiers) == 0 {
		return nil
	}
	updates := map[string]any{
		newAPIUsageWindowObservedExtraKey: 1.0,
	}
	for _, tier := range tiers {
		util := tier.UsedPercent / 100
		if util < 0 {
			util = 0
		}
		var resetUnix float64
		if tier.ResetAt != "" {
			if t, err := time.Parse(time.RFC3339, tier.ResetAt); err == nil {
				resetUnix = float64(t.UTC().Unix())
			}
		}
		sampled := now.UTC().Format(time.RFC3339Nano)
		switch tier.Window {
		case "5h":
			updates[newAPIFiveHourUtilExtraKey] = util
			updates[newAPIFiveHourSampledExtraKey] = sampled
			if resetUnix > 0 {
				updates[newAPIFiveHourResetExtraKey] = resetUnix
			}
			// Exhausted 5h → genuine account-wide lock (UI cool + scheduling).
			// Otherwise clear a stale lock so upgrade / window roll cannot leave
			// AccountWideRateLimitResetAt honoring an old cool after remains recovers.
			if util >= 0.999 && resetUnix > 0 {
				updates[NewAPIAccountWindowLockExtraKey] = 1.0
			} else {
				updates[NewAPIAccountWindowLockExtraKey] = nil
			}
		case "weekly":
			updates[newAPIWeeklyUtilExtraKey] = util
			updates[newAPIWeeklySampledExtraKey] = sampled
			if resetUnix > 0 {
				updates[newAPIWeeklyResetExtraKey] = resetUnix
			}
		}
	}
	return updates
}

// tkIsMiniMaxTokenPlanQuotaMessage reports the MiniMax Token Plan included-quota
// exhaustion copy (Chinese 用量上限 / code 2056). Must stay narrow: English
// "Token Plan Person monthly…" belongs to the Qianfan monthly owner.
func tkIsMiniMaxTokenPlanQuotaMessage(haystack string) bool {
	h := strings.ToLower(strings.TrimSpace(haystack))
	if h == "" {
		return false
	}
	if strings.Contains(h, "("+tkMiniMaxTokenPlanQuotaCode+")") {
		return true
	}
	if strings.Contains(h, "token plan") && strings.Contains(h, "用量上限") {
		return true
	}
	return false
}

// tkMiniMaxTokenPlanQuotaFallbackHit returns the 5h cool used when MiniMax Token
// Plan exhaustion is recognized but no reset text / Extra snapshot is available.
func tkMiniMaxTokenPlanQuotaFallbackHit(now time.Time) *newAPIUsageWindowHit {
	return &newAPIUsageWindowHit{
		Window:  "5h",
		ResetAt: now.Add(tkMiniMaxTokenPlanQuotaFallback),
	}
}

// tkMiniMaxTokenPlanHitFromExtra prefers a future newapi_5h / newapi_weekly
// snapshot already on the account (e.g. after an ops remains backfill).
func tkMiniMaxTokenPlanHitFromExtra(account *Account, now time.Time) *newAPIUsageWindowHit {
	if account == nil || account.Extra == nil {
		return nil
	}
	// Prefer the nearer exhausted window: 5h first when utilization is known-full
	// or reset is in the future; weekly otherwise.
	if reset, ok := tkParseNewAPIExtraUnixReset(account.Extra[newAPIFiveHourResetExtraKey]); ok && reset.After(now) {
		return &newAPIUsageWindowHit{Window: "5h", ResetAt: reset}
	}
	if reset, ok := tkParseNewAPIExtraUnixReset(account.Extra[newAPIWeeklyResetExtraKey]); ok && reset.After(now) {
		return &newAPIUsageWindowHit{Window: "weekly", ResetAt: reset}
	}
	return nil
}

func tkParseNewAPIExtraUnixReset(raw any) (time.Time, bool) {
	switch v := raw.(type) {
	case float64:
		if v <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(v), 0).UTC(), true
	case int64:
		if v <= 0 {
			return time.Time{}, false
		}
		return time.Unix(v, 0).UTC(), true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}, false
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC(), true
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}
