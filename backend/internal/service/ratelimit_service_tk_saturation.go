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
//
// Returns the new in-window count so callers can gate a sustained-only side
// effect (e.g. the prod mirror per-class cooldown) on the SAME threshold
// without a second Redis read. Returns 0 when the counter is unwired or Redis
// errored — callers MUST treat 0 as "cannot confirm sustained" (do NOT cool).
func (s *RateLimitService) recordAnthropicStubSaturation(ctx context.Context, accountID int64, statusCode int, reason string) int64 {
	if s == nil || s.anthropicSaturationCounter == nil {
		return 0
	}
	count, err := s.anthropicSaturationCounter.IncrementSaturation(ctx, accountID, edgeMirrorStubSaturationWindowSeconds)
	if err != nil {
		slog.Warn("anthropic_stub_saturation_increment_failed",
			"account_id", accountID,
			"reason", reason,
			"error", err)
		return 0
	}
	// Log exactly on the threshold-crossing increment (count == threshold), not
	// per hit — one line marks the transition into the de-prioritized state.
	if count == anthropicSaturationThreshold {
		slog.Info("anthropic_stub_saturated_deprioritized",
			"account_id", accountID,
			"recent_count", count,
			"threshold", anthropicSaturationThreshold,
			"window_seconds", edgeMirrorStubSaturationWindowSeconds,
			"status_code", statusCode,
			"reason", reason)
	}
	return count
}
