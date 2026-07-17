package service

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// defaultAnthropicMaxRateLimitCooldownSeconds is the DEFAULT-ON ceiling (5h)
// applied to Anthropic unified-window (5h/7d) 429 cooldowns when the operator
// has not configured SettingKeyAnthropicMaxRateLimitCooldownSeconds. An explicit
// "0" disables clamping (trust the upstream reset verbatim).
const defaultAnthropicMaxRateLimitCooldownSeconds = 18000

// AnthropicMaxRateLimitCooldownSeconds returns the ceiling (seconds) for how long
// an Anthropic account may stay rate-limited from a single upstream unified-window
// 429 reset.
//
// Unlike its OpenAI twin (OpenAIMaxRateLimitCooldownSeconds), this is DEFAULT-ON
// with the same default ceiling (18000s / 5h): an unset / blank / non-numeric /
// negative value falls back to defaultAnthropicMaxRateLimitCooldownSeconds. Only
// an explicit, non-negative integer overrides it; "0" disables clamping.
//
// TK (sibling of upstream Wei-Shaw/sub2api#1981; see
// ratelimit_service_tk_anthropic_reset_clamp.go package doc).
func (s *SettingService) AnthropicMaxRateLimitCooldownSeconds(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return defaultAnthropicMaxRateLimitCooldownSeconds
	}
	vals, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyAnthropicMaxRateLimitCooldownSeconds})
	if err != nil {
		return defaultAnthropicMaxRateLimitCooldownSeconds
	}
	raw := strings.TrimSpace(vals[SettingKeyAnthropicMaxRateLimitCooldownSeconds])
	if raw == "" {
		return defaultAnthropicMaxRateLimitCooldownSeconds
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		// Malformed config must not silently disable a safety default — fall back
		// to the default-on ceiling rather than to 0 (which would mean "trust the
		// multi-day upstream reset", the exact bug this guards against).
		return defaultAnthropicMaxRateLimitCooldownSeconds
	}
	return v
}

// tkClampAnthropicWindowReset clamps an upstream-provided Anthropic unified-window
// 429 reset time to now+ceiling.
//
// TK: Anthropic's unified 5h/7d windows are rolling — a 429 fired when utilization
// momentarily hit >=1.0 carries a reset header pointing at the conservative window
// boundary (a 7d reset can be days out), but the account's rolling utilization
// often drops back below the limit well before then as old usage ages out. Trusting
// the reset verbatim benches a recovered account until the boundary; on a thin/SPOF
// edge that is a multi-day anthropic outage (prod 2026-06, edge-us6 oh-3-a). Clamping
// the SCHEDULING cooldown lets natural request traffic re-probe the account after the
// ceiling — a still-exhausted window simply 429s again and is re-cooled ("traffic as
// the probe": no background prober, and overage=org_level_disabled means a re-probe
// just 429s without consuming tokens).
//
// This only affects the cooldown reset passed to SetRateLimited. The passive usage
// gauges (session_window / passive_usage_*) are written from the ORIGINAL upstream
// window values by the callers, so the operator-facing utilization view is unchanged.
func (s *RateLimitService) tkClampAnthropicWindowReset(ctx context.Context, accountID int64, resetAt time.Time) time.Time {
	if s == nil || s.settingService == nil {
		return resetAt
	}
	maxSeconds := s.settingService.AnthropicMaxRateLimitCooldownSeconds(ctx)
	if maxSeconds <= 0 {
		return resetAt
	}
	ceiling := time.Now().Add(time.Duration(maxSeconds) * time.Second)
	if resetAt.After(ceiling) {
		slog.Info("anthropic_rate_limit_reset_clamped",
			"account_id", accountID,
			"original_reset", resetAt,
			"clamped_reset", ceiling,
			"max_cooldown_seconds", maxSeconds)
		return ceiling
	}
	return resetAt
}
