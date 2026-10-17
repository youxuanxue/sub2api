package service

import (
	"context"
	"log/slog"
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
// failover-terminal envelope). Best-effort: Redis errors are
// logged and swallowed — selection must never fail because the preference
// counter is unavailable. Logs at threshold crossing only (low-volume), so ops
// can see a stub entering the de-prioritized state.
//
// Returns the new in-window count (best-effort). Returns 0 when the counter is
// unwired or Redis errored.
func (s *RateLimitService) recordAnthropicStubSaturation(ctx context.Context, accountID int64, statusCode int, reason string) int64 {
	if s == nil || s.anthropicSaturationCounter == nil {
		return 0
	}
	count, err := s.anthropicSaturationCounter.IncrementSaturation(ctx, accountID, saturationWindowSeconds)
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
			"window_seconds", saturationWindowSeconds,
			"status_code", statusCode,
			"reason", reason)
	}
	return count
}
