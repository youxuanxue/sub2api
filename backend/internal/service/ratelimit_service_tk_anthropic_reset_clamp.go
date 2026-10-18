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
