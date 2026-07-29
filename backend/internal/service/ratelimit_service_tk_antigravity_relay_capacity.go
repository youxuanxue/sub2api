package service

import (
	"context"
	"log/slog"
)

func (s *RateLimitService) SetAntigravitySaturationCounter(cache AntigravitySaturationCounterCache) {
	s.antigravitySaturationCounter = cache
}

func (s *RateLimitService) recordAntigravityRelaySaturation(
	ctx context.Context,
	accountID int64,
	modelKey string,
	statusCode int,
) int64 {
	if s == nil || s.antigravitySaturationCounter == nil {
		return 0
	}
	count, err := s.antigravitySaturationCounter.IncrementSaturation(
		ctx,
		accountID,
		modelKey,
		edgeMirrorStubSaturationWindowSeconds,
	)
	if err != nil {
		slog.Warn("antigravity_relay_saturation_increment_failed",
			"account_id", accountID,
			"model_key", modelKey,
			"error", err)
		return 0
	}
	if count == edgeMirrorStubSaturationThreshold {
		slog.Info("antigravity_relay_saturated",
			"account_id", accountID,
			"model_key", modelKey,
			"recent_count", count,
			"threshold", edgeMirrorStubSaturationThreshold,
			"window_seconds", edgeMirrorStubSaturationWindowSeconds,
			"status_code", statusCode)
	}
	return count
}
