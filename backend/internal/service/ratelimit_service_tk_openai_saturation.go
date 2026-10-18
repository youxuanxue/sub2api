package service

import (
	"context"
	"log/slog"
)

func (s *RateLimitService) SetOpenAISaturationCounter(cache OpenAISaturationCounterCache) {
	s.openaiSaturationCounter = cache
}

func (s *RateLimitService) recordOpenAIStubSaturation(ctx context.Context, accountID int64, statusCode int, reason string) int64 {
	if s == nil || s.openaiSaturationCounter == nil {
		return 0
	}
	count, err := s.openaiSaturationCounter.IncrementSaturation(ctx, accountID, saturationWindowSeconds)
	if err != nil {
		slog.Warn("openai_stub_saturation_increment_failed",
			"account_id", accountID,
			"reason", reason,
			"error", err)
		return 0
	}
	if count == saturationThreshold {
		slog.Info("openai_stub_saturated_deprioritized",
			"account_id", accountID,
			"recent_count", count,
			"threshold", saturationThreshold,
			"window_seconds", saturationWindowSeconds,
			"status_code", statusCode,
			"reason", reason)
	}
	return count
}
