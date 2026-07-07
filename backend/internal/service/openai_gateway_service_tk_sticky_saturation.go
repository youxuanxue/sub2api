package service

import (
	"context"
	"log/slog"
)

// tkShouldClearOpenAIStickyForSaturation clears a sticky binding when the bound
// OpenAI edge-mirror stub is SUSTAINEDLY returning downstream capacity signals,
// mirroring gateway_service_tk_sticky_saturation.go for the GPT scheduler.
func (s *OpenAIGatewayService) tkShouldClearOpenAIStickyForSaturation(ctx context.Context, account *Account, sessionHash string) bool {
	if s == nil || s.tkOpenAISaturationCounter == nil || account == nil {
		return false
	}
	if !tkIsOpenAIEdgeMirrorStub(account) {
		return false
	}
	if s.settingService != nil && !s.settingService.IsOpenAISaturatedStubDeprioritizeEnabled(ctx) {
		return false
	}
	counts, err := s.tkOpenAISaturationCounter.GetSaturationBatch(ctx, []int64{account.ID})
	if err != nil {
		return false
	}
	count := counts[account.ID]
	if count < openAIEdgeMirrorStubSaturationThreshold {
		return false
	}
	slog.Info("openai_sticky_cleared_saturated_stub",
		"account_id", account.ID,
		"recent_count", count,
		"threshold", openAIEdgeMirrorStubSaturationThreshold,
		"window_seconds", edgeMirrorStubSaturationWindowSeconds,
		"session", shortSessionHash(sessionHash),
	)
	return true
}
