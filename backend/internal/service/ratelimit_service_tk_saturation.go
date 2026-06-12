package service

import (
	"context"
	"log/slog"
)

// TK — anthropic saturated mirror-stub de-prioritization (increment side).
//
// Problem (prod): prod reaches Anthropic ONLY through per-edge api-key mirror
// "stub" accounts (cc-us2 / cc-uk1 / …) that forward to the matching Edge
// gateway, where a SINGLE OAuth account is common (SPOF). When that edge's one
// account is upstream-rate-limited (~47-min window), the edge returns TokenKey's
// own "No available accounts" 429 (or "all available accounts exhausted" 502).
// tkSkipDownstreamNoAvailableAccountsPenalty / ...FailoverExhaustedPenalty
// correctly SKIP penalty + fail over (so users see no failures) but DELIBERATELY
// keep the stub fully schedulable — counting these toward the 3/3 ladder once
// caused the 2026-05-31 "503 amplifier" that collapsed the whole prod pool.
//
// Consequence: prod keeps selecting the dead stub FIRST and paying a wasted
// failover hop on EVERY request for the whole window. This counter feeds a
// BOUNDED scheduler preference (gateway_service_tk_saturation_penalty.go) that
// routes AWAY from a SUSTAINEDLY saturated stub — a preference, NOT a cooldown:
// it never SetTempUnschedulable / advances the ladder / SetRateLimited.

const (
	// anthropicSaturationWindowSeconds is the fixed-window TTL of the per-stub
	// saturation counter. ~90s: long enough that a real ~47-min upstream-limit
	// window keeps the count continuously above threshold (each forwarded request
	// re-increments), short enough that a recovered edge clears the preference
	// within ~1.5 min of its last no-available hit (self-clearing, no 200-hook).
	anthropicSaturationWindowSeconds = 90

	// anthropicSaturationThreshold is the in-window hit count at/above which a
	// stub is treated as "saturated" and de-prioritized. A small threshold (not
	// 1) preserves current behaviour for transient blips — a couple of stray
	// no-available hits (e.g. one momentary edge burst) never trip it; only a
	// SUSTAINED pattern does.
	anthropicSaturationThreshold int64 = 4
)

// SetAnthropicSaturationCounter wires the Redis-backed saturation counter into
// RateLimitService (optional dependency). Nil-safe: when unset, the increment
// helper is a no-op and the feature is inert.
func (s *RateLimitService) SetAnthropicSaturationCounter(cache AnthropicSaturationCounterCache) {
	s.anthropicSaturationCounter = cache
}

// recordAnthropicStubSaturation increments the per-stub saturation counter for a
// downstream-capacity hit. Called only from the two skip-penalty branches in
// HandleUpstreamError (anthropic stub received our own "no available accounts" /
// "all available accounts exhausted" envelope). Best-effort: Redis errors are
// logged and swallowed — selection must never fail because the preference
// counter is unavailable. Logs at threshold crossing only (low-volume), so ops
// can see a stub entering the de-prioritized state.
func (s *RateLimitService) recordAnthropicStubSaturation(ctx context.Context, accountID int64, statusCode int, reason string) {
	if s == nil || s.anthropicSaturationCounter == nil {
		return
	}
	count, err := s.anthropicSaturationCounter.IncrementSaturation(ctx, accountID, anthropicSaturationWindowSeconds)
	if err != nil {
		slog.Warn("anthropic_stub_saturation_increment_failed",
			"account_id", accountID,
			"reason", reason,
			"error", err)
		return
	}
	// Log exactly on the threshold-crossing increment (count == threshold), not
	// per hit — one line marks the transition into the de-prioritized state.
	if count == anthropicSaturationThreshold {
		slog.Info("anthropic_stub_saturated_deprioritized",
			"account_id", accountID,
			"recent_count", count,
			"threshold", anthropicSaturationThreshold,
			"window_seconds", anthropicSaturationWindowSeconds,
			"status_code", statusCode,
			"reason", reason)
	}
}
