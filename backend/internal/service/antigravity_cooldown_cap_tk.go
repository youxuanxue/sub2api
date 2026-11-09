package service

import (
	"log/slog"
	"time"
)

// antigravityMaxModelCooldown caps every upstream-supplied Antigravity wait
// (RetryInfo.retryDelay, quotaResetDelay) before it becomes a model or account
// cooldown. Google sometimes returns hours or days ("Resets in 158h"), and prod
// has seen accounts still cooling with remainingFraction=1 because the quota
// reset early. A capped cooldown costs one re-probe request per window when
// the quota really is gone; an uncapped one can park a healthy account for a
// week in a pool of 10-15 accounts. The value mirrors the existing 429 cap
// (maxRateLimit429CooldownSeconds, 2h) and upstream PR #2302.
const antigravityMaxModelCooldown = time.Duration(maxRateLimit429CooldownSeconds) * time.Second

// clampAntigravityCooldown returns d capped at antigravityMaxModelCooldown.
// Values at or below the cap pass through unchanged.
func clampAntigravityCooldown(d time.Duration, source string) time.Duration {
	if d <= antigravityMaxModelCooldown {
		return d
	}
	slog.Info("antigravity_upstream_cooldown_clamped",
		"source", source,
		"upstream_delay", d.String(),
		"clamped_to", antigravityMaxModelCooldown.String())
	return antigravityMaxModelCooldown
}

// clampAntigravityResetAt caps an absolute reset time at now+antigravityMaxModelCooldown.
func clampAntigravityResetAt(resetAt, now time.Time, source string) time.Time {
	if resetAt.Sub(now) <= antigravityMaxModelCooldown {
		return resetAt
	}
	return now.Add(clampAntigravityCooldown(resetAt.Sub(now), source))
}
