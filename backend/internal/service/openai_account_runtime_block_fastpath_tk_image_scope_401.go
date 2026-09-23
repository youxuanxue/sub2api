package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// tkHandleOpenAIImageScope401Fastpath cools openai:image_generation when an
// OpenAI OAuth/API account rejects the request for missing image scopes
// (api.model.images.request). Returns true to failover without whole-account
// cooldown — mixed GPT专线 pools can still try accounts that retain image scope.
//
// Non-image capability-scope 401s are left to tkHandleAuth401 (skip penalty,
// no failover) so unrelated missing scopes do not poison the image pool.
func (s *OpenAIGatewayService) tkHandleOpenAIImageScope401Fastpath(
	stateCtx context.Context,
	account *Account,
	statusCode int,
	responseBody []byte,
) bool {
	if !tkIsImageCapabilityScope401(statusCode, responseBody) {
		return false
	}
	if account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	if s != nil && s.rateLimitService != nil && s.rateLimitService.accountRepo != nil {
		resetAt := time.Now().Add(openAIImageCapabilityLossTTL)
		if err := s.rateLimitService.accountRepo.SetModelRateLimit(
			stateCtx, account.ID, openAIImageGenerationRateLimitKey, resetAt, openAIImageCapabilityLossReason,
		); err != nil {
			slog.Warn("openai_image_scope_401_set_model_rate_limit_failed",
				"account_id", account.ID,
				"scope", openAIImageGenerationRateLimitKey,
				"error", err)
		} else {
			slog.Info("openai_image_capability_lost",
				"account_id", account.ID,
				"scope", openAIImageGenerationRateLimitKey,
				"reason", "capability_scope_401",
				"reset_at", resetAt)
		}
	}
	return true
}

// tkIsImageCapabilityScope401 is the image-specific subset of capability-scope
// 401: both generic capability-scope anchors plus an images.request scope mark.
func tkIsImageCapabilityScope401(statusCode int, body []byte) bool {
	if !tkIsCapabilityScope401(statusCode, body) {
		return false
	}
	hay := tkCapabilityScope401Haystack(body)
	return strings.Contains(hay, "api.model.images") ||
		strings.Contains(hay, "images.request") ||
		strings.Contains(hay, "model.images")
}
