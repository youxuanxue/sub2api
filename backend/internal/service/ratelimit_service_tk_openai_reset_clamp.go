package service

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// defaultOpenAIMaxRateLimitCooldownSeconds is the DEFAULT-ON ceiling (1h) applied
// to OpenAI-compat (OpenAI + NewAPI) window-exhaustion 429 cooldowns when the
// operator has not configured SettingKeyOpenAIMaxRateLimitCooldownSeconds. An
// explicit "0" disables clamping (trust the upstream reset verbatim). Kept in
// lockstep with defaultAnthropicMaxRateLimitCooldownSeconds.
const defaultOpenAIMaxRateLimitCooldownSeconds = 3600

// OpenAIMaxRateLimitCooldownSeconds returns the ceiling (seconds) for how long an
// OpenAI-compat account may stay rate-limited from a single upstream window 429
// reset.
//
// DEFAULT-ON, mirroring AnthropicMaxRateLimitCooldownSeconds: an unset / blank /
// non-numeric / negative value falls back to defaultOpenAIMaxRateLimitCooldownSeconds.
// Only an explicit, non-negative integer overrides it; "0" disables clamping.
//
// TK (upstream Wei-Shaw/sub2api#1981; default flipped ON for parity with the
// Anthropic clamp — both windows clear well before the upstream reset).
func (s *SettingService) OpenAIMaxRateLimitCooldownSeconds(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return defaultOpenAIMaxRateLimitCooldownSeconds
	}
	vals, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyOpenAIMaxRateLimitCooldownSeconds})
	if err != nil {
		return defaultOpenAIMaxRateLimitCooldownSeconds
	}
	raw := strings.TrimSpace(vals[SettingKeyOpenAIMaxRateLimitCooldownSeconds])
	if raw == "" {
		return defaultOpenAIMaxRateLimitCooldownSeconds
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		// Malformed config must not silently disable the safety default.
		return defaultOpenAIMaxRateLimitCooldownSeconds
	}
	return v
}

// tkClampOpenAIRateLimitReset clamps an upstream-provided OpenAI 429 reset time
// to now+ceiling (default-on; SettingKeyOpenAIMaxRateLimitCooldownSeconds=0
// disables it).
//
// TK (upstream Wei-Shaw/sub2api#1981): OpenAI window-exhaustion 429s carry a
// reset that can be hours or a full 7 days out (calculateOpenAI429ResetTime).
// Trusting it verbatim leaves an account idle for the entire window even after
// the upstream limit has actually cleared, while callers see "no available
// accounts" / 503 despite spare capacity. Clamping lets the account re-enter the
// pool after the ceiling so live traffic re-probes it (a fresh 429 re-cools it).
// This is "traffic as the probe" — no separate background prober, no extra
// upstream cost. Default-on (ceiling 1h) for parity with the Anthropic clamp;
// set the ceiling to 0 to disable and trust the upstream reset verbatim.
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
