package service

import (
	"log/slog"
)

// ratelimit_service_tk_openai_burst.go — TokenKey OpenAI 429 burst policy.
//
// Upstream Wei-Shaw/sub2api#2258 root cause: when an OpenAI Codex 429 reports
// neither the 5h nor 7d usage window at 100% used, but still returns
// `x-codex-*-reset-after-seconds` headers, those reset values describe the
// FULL window reset — potentially multiple days. The original logic took
// max(reset5h, reset7d) and applied it as the account cooldown, turning
// every transient throttle 429 into a multi-day 503 window for the affected
// account. The TK policy: treat that branch as a transient burst and fall
// back to the configurable short cooldown (default 5s), matching the
// Anthropic no-reset path.
//
// The hotspot file backend/internal/service/ratelimit_service.go only calls
// TkRecordOpenAI429BurstFallthrough; the policy + log marker live here so an
// upstream merge that rewrites calculateOpenAI429ResetTime cannot silently
// reintroduce the long-cooldown behavior without also touching this file.
// scripts/gateway-tk-sentinels.json pins both the call site (`TkRecordOpenAI429BurstFallthrough`)
// and the slog marker (`openai_429_burst_below_window_limits`) so the
// preflight + upstream-merge PR shape checks catch any revert.

// TkIsOpenAI429TransientBurst reports whether the normalized Codex usage
// snapshot represents a burst/throttle 429 rather than window exhaustion.
// Returns true when no window has BOTH `used >= 100` AND a reset value.
// Exported so account_test_service.go and future call sites can share the
// predicate instead of re-implementing it inline.
func TkIsOpenAI429TransientBurst(normalized *NormalizedCodexLimits) bool {
	if normalized == nil {
		return false
	}
	is7dExhausted := normalized.Used7dPercent != nil && *normalized.Used7dPercent >= 100 && normalized.Reset7dSeconds != nil
	is5hExhausted := normalized.Used5hPercent != nil && *normalized.Used5hPercent >= 100 && normalized.Reset5hSeconds != nil
	return !is7dExhausted && !is5hExhausted
}

// TkRecordOpenAI429BurstFallthrough emits the operator-visible log marker
// for a burst 429 fall-through. Logging here (not at the call site) keeps
// the upstream file delta to a single line and makes the slog tag a stable
// sentinel literal.
func TkRecordOpenAI429BurstFallthrough(normalized *NormalizedCodexLimits) {
	if normalized == nil {
		slog.Info("openai_429_burst_below_window_limits", "normalized", "nil")
		return
	}
	slog.Info("openai_429_burst_below_window_limits",
		"used_5h_percent", normalized.Used5hPercent,
		"used_7d_percent", normalized.Used7dPercent,
		"reset_5h_seconds", normalized.Reset5hSeconds,
		"reset_7d_seconds", normalized.Reset7dSeconds,
	)
}
