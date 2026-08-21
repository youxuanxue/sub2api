package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	openAIImageCapabilityLossReason = "openai_image_capability_lost"
	openAIImageCapabilityLossTTL    = 24 * time.Hour
)

// isOpenAIImageCapabilityLoss400 识别账号级生图能力丢失的 400。
// 调用方自己的 invalid_request（缺参、非法 size）不得命中。
func isOpenAIImageCapabilityLoss400(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest || len(body) == 0 {
		return false
	}
	hay := strings.ToLower(string(body))
	hasImage := strings.Contains(hay, "image_generation") ||
		strings.Contains(hay, "gpt-image") ||
		strings.Contains(hay, "image generation")
	if !hasImage {
		return false
	}
	for _, marker := range []string{
		"not supported",
		"not available",
		"not enabled",
		"unknown tool",
		"does not have access",
		"tool is not",
	} {
		if strings.Contains(hay, marker) {
			return true
		}
	}
	return false
}

// HandleOpenAIImageCapabilityLoss400 把账号从生图调度里摘掉，但不罚整号。
func (s *RateLimitService) HandleOpenAIImageCapabilityLoss400(ctx context.Context, account *Account, statusCode int, responseBody []byte) bool {
	if s == nil || account == nil || s.accountRepo == nil {
		return false
	}
	if account.Platform != PlatformOpenAI {
		return false
	}
	if !isOpenAIImageCapabilityLoss400(statusCode, responseBody) {
		return false
	}

	resetAt := time.Now().Add(openAIImageCapabilityLossTTL)
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, openAIImageGenerationRateLimitKey, resetAt, openAIImageCapabilityLossReason); err != nil {
		slog.Warn("openai_image_capability_loss_set_model_rate_limit_failed",
			"account_id", account.ID,
			"scope", openAIImageGenerationRateLimitKey,
			"error", err)
		return true
	}
	slog.Info("openai_image_capability_lost",
		"account_id", account.ID,
		"scope", openAIImageGenerationRateLimitKey,
		"reset_at", resetAt)
	return true
}
