package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// handleGeminiUpstreamError applies Gemini upstream account-state side effects
// (401/403/529 via RateLimitService; 429 account-level SetRateLimited with TK
// Code Assist per-model and OAuth cooldown hooks). Moved from the upstream-
// shaped gemini_messages_compat_service.go as a behavior-preserving companion.
func (s *GeminiMessagesCompatService) handleGeminiUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, body []byte) {
	// 遵守自定义错误码策略：未命中则跳过所有限流处理
	if !account.ShouldHandleErrorCode(statusCode) {
		return
	}
	if s.rateLimitService != nil && (statusCode == 401 || statusCode == 403 || statusCode == 529) {
		s.rateLimitService.HandleUpstreamError(ctx, account, statusCode, headers, body)
		return
	}
	if statusCode != 429 {
		return
	}
	// 池模式账号不写账号级限流：账号留在池内，由 failover / 同号重试消化 429。
	// 自定义错误码优先级高于池模式，开启后仍按其命中结果标记。
	if account.IsPoolMode() && !account.IsCustomErrorCodesEnabled() {
		return
	}

	oauthType := account.GeminiOAuthType()
	tierID := account.GeminiTierID()
	projectID := strings.TrimSpace(account.GetCredential("project_id"))
	isCodeAssist := account.IsGeminiCodeAssist()

	// TK: per-model rate limit for Code Assist 429s carrying ErrorInfo.metadata.model
	// (e.g. MODEL_CAPACITY_EXHAUSTED on a single model). See
	// gemini_messages_compat_service_tk_model_rate_limit.go for rationale.
	if s.tryGeminiCodeAssistApplyModelRateLimit(ctx, account, body) {
		return
	}

	resetAt := ParseGeminiRateLimitResetTime(body)
	if resetAt == nil {
		ra := s.tkGeminiDefaultRateLimitResetAt(ctx, account, oauthType, tierID, projectID, isCodeAssist)
		_ = s.accountRepo.SetRateLimited(ctx, account.ID, ra)
		return
	}

	// 使用解析到的重置时间
	resetTime := time.Unix(*resetAt, 0)
	_ = s.accountRepo.SetRateLimited(ctx, account.ID, resetTime)
	logger.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Account %d rate limited until %v (oauth_type=%s, tier=%s)",
		account.ID, resetTime, oauthType, tierID)
}
