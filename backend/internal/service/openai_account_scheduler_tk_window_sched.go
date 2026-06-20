package service

import (
	"context"
	"time"
)

// Window-aware soft scheduling for the OpenAI/GPT line — the twin of the
// Anthropic window-cost tri-state filter (GatewayService.isAccountSchedulable
// ForWindowCost / Account.CheckWindowCostSchedulability). It steers new
// load-balance traffic away from an account approaching its codex 5h/7d usage
// window BEFORE it 429s, cutting avoidable failover hops, while keeping the
// account available for its own sticky sessions (prompt-cache affinity).
//
// Two differences from the Anthropic twin, by necessity:
//   - Denominator is codex used-percent (the upstream's OWN limit), not a $
//     business budget with headroom below the real limit. Excluding near 100%
//     therefore risks emptying a thin pool into a worse "no available accounts"
//     429, so the OpenAI side adds a never-empty-pool fallback
//     (leastUtilizedOpenAIAccount) that the Anthropic twin does not need.
//   - Signal source is account.Extra codex_5h/7d_used_percent (already captured
//     per-response and mirrored into the scheduler cache), read via the existing
//     resolveOpenAIQuotaUtilization with its used<=0 / window-reset / >2h-stale
//     guards. Accounts with no codex snapshot (newapi/compat, openai-apikey)
//     produce no signal and are never restricted (fail-open).
const (
	// Built-in defaults (the guard is default-ON). util at/below the threshold
	// => fully schedulable; threshold..threshold+reserve => sticky-only (the
	// account keeps serving its own sticky sessions but takes no new
	// load-balance traffic); above that => avoided, unless it is the only
	// headroom left in the pool. Operators override via the ops settings or
	// per-account Extra; see resolveOpenAIWindowStickyThresholds.
	openAIWindowStickyThresholdDefault = 0.85
	openAIWindowStickyReserveDefault   = 0.12
)

// openAIAccountWindowUtilization returns the WORSE (higher) of the account's 5h
// and 7d codex utilization in [0,1], and ok=false when there is no usable signal
// in either window. resolveOpenAIQuotaUtilization already returns ok=false for a
// missing snapshot, a window that has already reset, a >2h-stale snapshot, and
// used<=0, so all of those inherit the fail-open contract here.
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

// CheckOpenAIWindowSchedulability mirrors Account.CheckWindowCostSchedulability
// but keys off codex used-percent utilization instead of $ window cost:
//   - util < stickyThreshold                     -> Schedulable
//   - stickyThreshold <= util < +stickyReserve   -> StickyOnly
//   - util >= stickyThreshold + stickyReserve     -> NotSchedulable
//
// A non-positive or out-of-range stickyThreshold disables the restriction.
func CheckOpenAIWindowSchedulability(util, stickyThreshold, stickyReserve float64) WindowCostSchedulability {
	if stickyThreshold <= 0 || stickyThreshold >= 1 {
		return WindowCostSchedulable
	}
	if util < stickyThreshold {
		return WindowCostSchedulable
	}
	if stickyReserve < 0 {
		stickyReserve = 0
	}
	if util < stickyThreshold+stickyReserve {
		return WindowCostStickyOnly
	}
	return WindowCostNotSchedulable
}

// resolveOpenAIWindowStickyThresholds resolves (threshold, reserve, enabled) for
// an account with precedence: per-account Extra override -> global ops setting ->
// built-in default. enabled=false short-circuits the guard (global kill-switch
// or per-account disable). Mirrors resolveOpenAIQuotaAutoPauseThresholds and the
// window_cost_limit / window_cost_sticky_reserve precedence on the Anthropic side.
//
// Note: the global override is read from the auto-pause settings carried on the
// request context (seeded once per selection by withOpenAIQuotaAutoPauseContext).
// On code paths that do not seed it the built-in defaults still apply, so the
// guard stays default-ON everywhere; only the operator override is context-gated.
func resolveOpenAIWindowStickyThresholds(ctx context.Context, account *Account) (threshold, reserve float64, enabled bool) {
	settings := openAIQuotaAutoPauseSettingsFromContext(ctx)
	if settings.WindowStickyGuardDisabled {
		return 0, 0, false
	}
	if account != nil && resolveAccountExtraBool(account.Extra, "openai_window_guard_disabled") {
		return 0, 0, false
	}

	threshold = openAIWindowStickyThresholdDefault
	reserve = openAIWindowStickyReserveDefault
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

// isAccountSchedulableForOpenAIWindow is the OpenAI twin of GatewayService.is
// AccountSchedulableForWindowCost. isSticky=true keeps StickyOnly accounts (a
// sticky-session re-validation); isSticky=false (fresh load-balance selection)
// drops them. Fail-open: no signal / disabled guard => schedulable.
func (s *OpenAIGatewayService) isAccountSchedulableForOpenAIWindow(ctx context.Context, account *Account, isSticky bool) bool {
	if account == nil {
		return true
	}
	threshold, reserve, enabled := resolveOpenAIWindowStickyThresholds(ctx, account)
	if !enabled {
		return true
	}
	util, ok := openAIAccountWindowUtilization(account, time.Now())
	if !ok {
		return true
	}
	switch CheckOpenAIWindowSchedulability(util, threshold, reserve) {
	case WindowCostStickyOnly:
		return isSticky
	case WindowCostNotSchedulable:
		return false
	default:
		return true
	}
}

// leastUtilizedOpenAIAccount returns the account with the most window headroom
// (lowest utilization) among a set that was dropped purely by the window guard.
// It backs the never-empty-pool fallback: when the guard would otherwise empty a
// non-empty candidate pool, the coolest dropped account is re-admitted so the
// request still routes (better than an empty-pool 429). No-signal accounts count
// as utilization 0 (max headroom).
func leastUtilizedOpenAIAccount(dropped []*Account, now time.Time) *Account {
	var best *Account
	bestUtil := 2.0 // utilization is in [0,1]; 2.0 is an above-range sentinel
	for _, acc := range dropped {
		util, ok := openAIAccountWindowUtilization(acc, now)
		if !ok {
			util = 0
		}
		if best == nil || util < bestUtil {
			bestUtil = util
			best = acc
		}
	}
	return best
}
