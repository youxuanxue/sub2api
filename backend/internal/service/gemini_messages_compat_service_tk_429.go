package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
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
		// Vertex 共享容量不足描述的是请求当时的上游容量，不是账号额度：
		// 账号保持可调度，由请求内的同号退避重试与换号消化。
		if account.IsVertexServiceAccount() && isVertexSharedCapacity429(body) {
			logger.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Account %d (Vertex service account, project=%s) shared capacity exhausted, account stays schedulable", account.ID, projectID)
			return
		}
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

// isVertexSharedCapacity429 判断 Vertex 429 是否为按量付费共享容量不足：RESOURCE_EXHAUSTED、
// 不带任何错误明细（RetryInfo / QuotaFailure / ErrorInfo），文案为 "Resource exhausted ... try again later"
// 且不涉及项目配额或预留吞吐。其他形态的 429 按账号自身限速处理。
func isVertexSharedCapacity429(body []byte) bool {
	errObj := gjson.GetBytes(body, "error")
	if !errObj.Exists() {
		errObj = gjson.GetBytes(body, "0.error")
	}
	if !errObj.IsObject() {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(errObj.Get("status").String()), "RESOURCE_EXHAUSTED") {
		return false
	}
	if len(errObj.Get("details").Array()) > 0 {
		return false
	}
	msg := strings.ToLower(errObj.Get("message").String())
	if !strings.Contains(msg, "resource exhausted") || !strings.Contains(msg, "try again later") {
		return false
	}
	return !strings.Contains(msg, "quota") && !strings.Contains(msg, "provisioned throughput")
}
