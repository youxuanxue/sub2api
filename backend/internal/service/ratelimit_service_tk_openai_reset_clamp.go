package service

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// OpenAIMaxRateLimitCooldownSeconds returns the opt-in ceiling (seconds) for how
// long an OpenAI-compat account may stay rate-limited from a single upstream 429
// reset. Returns 0 — disabled, trust the upstream reset verbatim — when unset,
// blank, non-numeric, or negative.
//
// TK (upstream Wei-Shaw/sub2api#1981).
func (s *SettingService) OpenAIMaxRateLimitCooldownSeconds(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return 0
	}
	vals, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyOpenAIMaxRateLimitCooldownSeconds})
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(vals[SettingKeyOpenAIMaxRateLimitCooldownSeconds]))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// tkClampOpenAIRateLimitReset clamps an upstream-provided OpenAI 429 reset time
// to now+ceiling when the opt-in SettingKeyOpenAIMaxRateLimitCooldownSeconds is
// configured.
//
// TK (upstream Wei-Shaw/sub2api#1981): OpenAI window-exhaustion 429s carry a
// reset that can be hours or a full 7 days out (calculateOpenAI429ResetTime).
// Trusting it verbatim leaves an account idle for the entire window even after
// the upstream limit has actually cleared, while callers see "no available
// accounts" / 503 despite spare capacity. Clamping lets the account re-enter the
// pool after the ceiling so live traffic re-probes it (a fresh 429 re-cools it).
// This is "traffic as the probe" — no separate background prober, no extra
// upstream cost, and strictly opt-in (ceiling 0 = disabled, no behavior change).
func (s *RateLimitService) tkClampOpenAIRateLimitReset(ctx context.Context, accountID int64, resetAt time.Time) time.Time {
	if s == nil || s.settingService == nil {
		return resetAt
	}
	maxSeconds := s.settingService.OpenAIMaxRateLimitCooldownSeconds(ctx)
	if maxSeconds <= 0 {
		return resetAt
	}
	ceiling := time.Now().Add(time.Duration(maxSeconds) * time.Second)
	if resetAt.After(ceiling) {
		slog.Info("openai_rate_limit_reset_clamped",
			"account_id", accountID,
			"original_reset", resetAt,
			"clamped_reset", ceiling,
			"max_cooldown_seconds", maxSeconds)
		return ceiling
	}
	return resetAt
}
