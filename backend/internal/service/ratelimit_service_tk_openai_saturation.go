package service

import (
	"context"
	"log/slog"
)

// TK — OpenAI edge-mirror stub de-prioritization (increment side). Mirrors
// ratelimit_service_tk_saturation.go for the GPT line: prod openai-us* apikey
// stubs forward to api-us*.tokenkey.dev; when the edge pool is momentarily empty
// the stub receives our own "No available accounts" 429. Fail over without
// cooling the stub, but feed this counter so the scheduler stops picking the
// saturated stub first on every request.

func (s *RateLimitService) SetOpenAISaturationCounter(cache OpenAISaturationCounterCache) {
	s.openaiSaturationCounter = cache
}

func (s *RateLimitService) recordOpenAIStubSaturation(ctx context.Context, accountID int64, statusCode int, reason string) int64 {
	if s == nil || s.openaiSaturationCounter == nil {
		return 0
	}
	count, err := s.openaiSaturationCounter.IncrementSaturation(ctx, accountID, anthropicSaturationWindowSeconds)
	if err != nil {
		slog.Warn("openai_stub_saturation_increment_failed",
			"account_id", accountID,
			"reason", reason,
			"error", err)
		return 0
	}
	if count == anthropicSaturationThreshold {
		slog.Info("openai_stub_saturated_deprioritized",
			"account_id", accountID,
			"recent_count", count,
			"threshold", anthropicSaturationThreshold,
			"window_seconds", anthropicSaturationWindowSeconds,
			"status_code", statusCode,
			"reason", reason)
	}
	return count
}
