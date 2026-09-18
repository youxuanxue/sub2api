package service

import (
	"context"
	"log/slog"
)

// TK — OpenAI soft scheduling de-prioritization (increment side).
//
// Writers:
//   - prod openai-us* edge-mirror stubs: downstream-empty envelopes
//     (ratelimit_service_tk_openai_downstream.go) and sanitized edge capacity
//     502/503 (openai_capacity_saturation_tk.go)
//   - edge OpenAI OAuth / setup-token: native upstream overloaded / 503
//     temporarily unavailable (openai_capacity_saturation_tk.go)
//
// Fail over without cooling the account; the counter only nudges the scheduler
// away from the hot account/stub for the rolling window in
// edge_mirror_stub_saturation_tk.go.

func (s *RateLimitService) SetOpenAISaturationCounter(cache OpenAISaturationCounterCache) {
	s.openaiSaturationCounter = cache
}

func (s *RateLimitService) recordOpenAIStubSaturation(ctx context.Context, accountID int64, statusCode int, reason string) int64 {
	if s == nil || s.openaiSaturationCounter == nil {
		return 0
	}
	count, err := s.openaiSaturationCounter.IncrementSaturation(ctx, accountID, edgeMirrorStubSaturationWindowSeconds)
	if err != nil {
		slog.Warn("openai_stub_saturation_increment_failed",
			"account_id", accountID,
			"reason", reason,
			"error", err)
		return 0
	}
	if count == openAIEdgeMirrorStubSaturationThreshold {
		slog.Info("openai_stub_saturated_deprioritized",
			"account_id", accountID,
			"recent_count", count,
			"threshold", openAIEdgeMirrorStubSaturationThreshold,
			"window_seconds", edgeMirrorStubSaturationWindowSeconds,
			"status_code", statusCode,
			"reason", reason)
	}
	return count
}
